package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azazo1/download-cli/internal/httpx"
)

// File 是下载目标文件需要提供的能力.
type File interface {
	io.WriterAt
	Sync() error
}

// Options 是一个下载任务的参数.
type Options struct {
	URL       string
	Size      int64
	Resumable bool
	File      File
	// Parts 为续传时恢复的分片, 为空表示新下载.
	Parts        []PartState
	Connections  int
	MinPartSize  int64
	DynamicSplit bool
	// Retries 是单个分片在没有进展时允许的连续失败次数.
	Retries      int
	StallTimeout time.Duration
	// RetryBaseDelay 是首次重试的等待时间, 之后翻倍, 最长 10 倍.
	RetryBaseDelay time.Duration
	SaveInterval   time.Duration
	// Save 持久化分片快照, 可以为 nil.
	Save   func([]PartState) error
	Logger *slog.Logger
}

// Stats 是任务结束时的统计.
type Stats struct {
	Splits  int
	Retries int
	// Reused 是续传时已有的字节数.
	Reused int64
}

// Job 是一个运行中的下载任务.
type Job struct {
	client *httpx.Client
	opts   Options
	log    *slog.Logger

	// mu 保护所有分片的 to 与 safeZone.
	mu    sync.Mutex
	parts []*Part

	downloaded  atomic.Int64
	activeCount atomic.Int32

	// 以下字段只在事件循环 goroutine 中访问.
	active    map[*Part]*worker
	failures  map[*Part]int
	retryAt   map[*Part]time.Time
	target    int
	throttled time.Time
	// exhausted 是被暂时放弃的分片: 它们反复失败, 但不应因此终止整个任务.
	exhausted map[*Part]error
	revives   int
	stats     Stats
	events    chan result
	wake      chan struct{}
	wg        sync.WaitGroup
}

type result struct {
	w   *worker
	err error
}

// NewJob 创建任务并初始化分片.
func NewJob(client *httpx.Client, opts Options) *Job {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.RetryBaseDelay <= 0 {
		opts.RetryBaseDelay = time.Second
	}
	if opts.SaveInterval <= 0 {
		opts.SaveInterval = 2 * time.Second
	}
	if opts.StallTimeout <= 0 {
		opts.StallTimeout = 30 * time.Second
	}
	opts.Connections = max(opts.Connections, 1)
	if !opts.Resumable || opts.Size < 0 {
		opts.Connections = 1
		opts.DynamicSplit = false
	}
	j := &Job{
		client:    client,
		opts:      opts,
		log:       opts.Logger,
		active:    map[*Part]*worker{},
		failures:  map[*Part]int{},
		retryAt:   map[*Part]time.Time{},
		exhausted: map[*Part]error{},
		target:    opts.Connections,
		events:    make(chan result, 8),
		wake:      make(chan struct{}, 1),
	}
	j.initParts()
	return j
}

func (j *Job) initParts() {
	states := j.opts.Parts
	switch {
	case len(states) > 0:
		j.stats.Reused = 0
		for _, s := range states {
			j.stats.Reused += s.Current - s.From
		}
	case j.opts.Size < 0:
		states = []PartState{{From: 0, To: -1, Current: 0}}
	case !j.opts.Resumable:
		states = []PartState{{From: 0, To: j.opts.Size - 1, Current: 0}}
	default:
		states = SplitToRange(j.opts.Size, j.opts.MinPartSize, j.opts.Connections)
	}
	for _, s := range states {
		j.parts = append(j.parts, newPart(s.From, s.To, s.Current))
	}
	j.downloaded.Store(j.stats.Reused)
}

// Downloaded 返回已写入的总字节数 (含续传部分).
func (j *Job) Downloaded() int64 { return j.downloaded.Load() }

// Active 返回当前活动连接数.
func (j *Job) Active() int { return int(j.activeCount.Load()) }

// PartCount 返回初始分片数.
func (j *Job) PartCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.parts)
}

// Snapshot 返回所有分片的快照.
func (j *Job) Snapshot() []PartState {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]PartState, len(j.parts))
	for i, p := range j.parts {
		out[i] = p.stateLocked()
	}
	return out
}

// Stats 返回统计信息, 应在 Run 返回后调用.
func (j *Job) Stats() Stats { return j.stats }

// Run 执行下载直到完成, 出现致命错误或 ctx 被取消.
// ctx 被取消时返回 ctx.Err(), 分片状态已保存, 可以续传.
func (j *Job) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ticker := time.NewTicker(j.opts.SaveInterval)
	defer ticker.Stop()

	j.schedule(runCtx)
	for {
		if ctx.Err() != nil {
			return j.finish(cancel, ctx.Err())
		}
		if j.allCompleted() {
			return j.finish(cancel, nil)
		}
		if len(j.active) == 0 && len(j.retryAt) == 0 {
			if err := j.tryRevive(); err != nil {
				return j.finish(cancel, err)
			}
		}
		select {
		case <-ctx.Done():
			return j.finish(cancel, ctx.Err())
		case <-ticker.C:
			j.save()
		case <-j.wake:
			j.schedule(runCtx)
		case r := <-j.events:
			if err := j.handle(runCtx, r); err != nil {
				return j.finish(cancel, err)
			}
			j.schedule(runCtx)
		}
	}
}

// label 返回分片区间的可读形式, 可安全地在任意 goroutine 调用.
func (j *Job) label(p *Part) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return p.rangeStr()
}

// handle 处理一个 worker 的结束事件, 返回非 nil 表示任务应当中止.
func (j *Job) handle(ctx context.Context, r result) error {
	p := r.w.part
	delete(j.active, p)
	j.activeCount.Add(-1)
	if r.err == nil {
		delete(j.failures, p)
		j.log.Debug("分片完成", "分片", j.label(p))
		// 一段时间没有限流后逐步恢复并发连接数.
		if j.target < j.opts.Connections && time.Since(j.throttled) > 30*time.Second {
			j.target++
			j.log.Info("恢复并发连接数", "连接数", j.target)
		}
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if isFatal(r.err) {
		return r.err
	}
	j.stats.Retries++
	if p.Current() > r.w.startCurrent {
		j.failures[p] = 1
	} else {
		j.failures[p]++
	}
	n := j.failures[p]
	throttling := isThrottled(r.err)
	// 多个连接同时被限流时只降低一级, 避免并发数骤降.
	if throttling && len(j.active) > 0 && j.target > 1 && time.Since(j.throttled) > 2*time.Second {
		j.target--
		j.throttled = time.Now()
		j.log.Warn("服务器限流, 降低并发连接数", "连接数", j.target, "原因", r.err)
	}
	// 限流是暂时状态, 放宽重试次数, 但仍有上限, 不会无限重试.
	limit := j.opts.Retries
	if throttling {
		limit *= 5
	}
	if n > limit {
		// 先放弃这个分片, 让其它分片继续; 全部无法推进时再统一重试或失败.
		j.exhausted[p] = r.err
		j.log.Warn("分片反复失败, 暂时跳过", "分片", j.label(p), "次数", n, "错误", r.err)
		return nil
	}
	delay := j.opts.RetryBaseDelay << min(n-1, 3)
	delay = min(delay, 10*j.opts.RetryBaseDelay)
	if ra := retryAfter(r.err); ra > 0 {
		// 尊重服务器建议, 但不要等太久, 降级后的低并发通常能立即成功.
		delay = min(ra, 5*time.Second)
	}
	j.log.Warn("分片下载出错, 稍后重试", "分片", j.label(p), "第几次", n, "等待", delay, "错误", r.err)
	j.retryAt[p] = time.Now().Add(delay)
	time.AfterFunc(delay, func() {
		select {
		case j.wake <- struct{}{}:
		default:
		}
	})
	return nil
}

// schedule 补足活动连接: 先拿空闲分片, 没有时动态分裂.
func (j *Job) schedule(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	for len(j.active) < j.target {
		p := j.nextIdle(now)
		if p == nil && j.opts.DynamicSplit {
			p = j.splitOne()
		}
		if p == nil {
			return
		}
		j.start(ctx, p)
	}
}

func (j *Job) nextIdle(now time.Time) *Part {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, p := range j.parts {
		if _, ok := j.active[p]; ok || p.completedLocked() {
			continue
		}
		if _, ok := j.exhausted[p]; ok {
			continue
		}
		if at, ok := j.retryAt[p]; ok {
			if now.Before(at) {
				continue
			}
			delete(j.retryAt, p)
		}
		return p
	}
	return nil
}

// tryRevive 在所有分片都无法推进时, 给被放弃的分片新的机会.
// 仍无进展则返回导致失败的错误.
func (j *Job) tryRevive() error {
	if len(j.exhausted) == 0 {
		return errors.New("没有可以继续下载的分片")
	}
	const maxRevives = 2
	if j.revives >= maxRevives {
		for p, err := range j.exhausted {
			return fmt.Errorf("分片 %s 反复失败: %w", j.label(p), err)
		}
	}
	j.revives++
	delay := time.Duration(j.revives) * 5 * time.Second
	j.log.Warn("所有剩余分片都反复失败, 稍后重新尝试", "分片数", len(j.exhausted), "第几次", j.revives, "等待", delay)
	for p := range j.exhausted {
		delete(j.exhausted, p)
		delete(j.failures, p)
		j.retryAt[p] = time.Now().Add(delay)
	}
	time.AfterFunc(delay, func() {
		select {
		case j.wake <- struct{}{}:
		default:
		}
	})
	return nil
}

// splitOne 在活动分片里找剩余最多且值得分裂的, 切出后半段.
func (j *Job) splitOne() *Part {
	j.mu.Lock()
	defer j.mu.Unlock()
	candidates := make([]*Part, 0, len(j.active))
	for p := range j.active {
		candidates = append(candidates, p)
	}
	slices.SortFunc(candidates, func(a, b *Part) int {
		return int(min(max(b.remainingLocked()-a.remainingLocked(), -1), 1))
	})
	for _, p := range candidates {
		old := p.rangeStr()
		np := p.splitLocked(j.opts.MinPartSize)
		if np == nil {
			continue
		}
		j.parts = append(j.parts, np)
		slices.SortFunc(j.parts, func(a, b *Part) int { return int(min(max(a.From-b.From, -1), 1)) })
		j.stats.Splits++
		j.log.Debug("动态分裂", "原分片", old, "保留", p.rangeStr(), "新分片", np.rangeStr())
		return np
	}
	return nil
}

func (j *Job) start(ctx context.Context, p *Part) {
	wctx, cancel := context.WithCancelCause(ctx)
	w := &worker{part: p, startCurrent: p.Current(), cancel: cancel}
	j.active[p] = w
	j.activeCount.Add(1)
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		err := j.download(wctx, w)
		cancel(nil)
		j.events <- result{w: w, err: err}
	}()
}

func (j *Job) allCompleted() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, p := range j.parts {
		if !p.completedLocked() {
			return false
		}
	}
	return true
}

// finish 停止全部 worker, 刷盘并保存状态, 返回 err.
func (j *Job) finish(cancel context.CancelFunc, err error) error {
	cancel()
	for len(j.active) > 0 {
		r := <-j.events
		delete(j.active, r.w.part)
		j.activeCount.Add(-1)
	}
	j.wg.Wait()
	if serr := j.opts.File.Sync(); serr != nil && err == nil {
		err = fmt.Errorf("刷新文件到磁盘失败: %w", serr)
	}
	j.save()
	return err
}

func (j *Job) save() {
	if j.opts.Save == nil {
		return
	}
	if err := j.opts.Save(j.Snapshot()); err != nil {
		j.log.Warn("保存续传状态失败", "错误", err)
		return
	}
	j.log.Debug("续传状态已保存")
}
