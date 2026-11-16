package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/azazo1/download-cli/internal/httpx"
)

const readBufferSize = 32 * 1024

// worker 是一个分片的一次连接尝试.
type worker struct {
	part *Part
	// startCurrent 用于判断这次尝试是否有进展.
	startCurrent int64
	cancel       context.CancelCauseFunc
}

// download 连接服务器并把分片数据写入文件, 直到分片写满, 出错或被取消.
// 返回 nil 表示分片已完成.
func (j *Job) download(ctx context.Context, w *worker) error {
	p := w.part
	var lastData atomic.Int64
	lastData.Store(time.Now().UnixNano())
	stop := make(chan struct{})
	defer close(stop)
	go j.watchStall(stop, &lastData, w.cancel)

	j.mu.Lock()
	if p.completedLocked() {
		j.mu.Unlock()
		return nil
	}
	if !j.opts.Resumable && p.Current() != p.From {
		// 不支持 Range 时只能从头再来.
		j.downloaded.Add(-(p.Current() - p.From))
		p.current.Store(p.From)
		p.safeZone = p.From - 1
	}
	cur, to := p.Current(), p.to
	j.mu.Unlock()

	var resp *http.Response
	var err error
	if j.opts.Resumable {
		resp, err = j.client.Get(ctx, j.opts.URL, cur, to)
	} else {
		resp, err = j.client.Get(ctx, j.opts.URL, -1, -1)
	}
	if err != nil {
		return causeOf(ctx, err)
	}
	defer resp.Body.Close()
	if j.opts.Resumable {
		if err := j.verify(resp, cur); err != nil {
			return err
		}
	}
	j.log.Debug("分片已连接", "分片", fmt.Sprintf("[%d-%d]", cur, to), "状态码", resp.StatusCode)

	buf := make([]byte, readBufferSize)
	for {
		j.mu.Lock()
		n := p.canReadLocked(int64(len(buf)))
		j.mu.Unlock()
		if n == 0 {
			return nil
		}
		rn, rerr := resp.Body.Read(buf[:n])
		if rn > 0 {
			if _, werr := j.opts.File.WriteAt(buf[:rn], cur); werr != nil {
				return fatal(fmt.Errorf("写入文件失败: %w", werr))
			}
			cur += int64(rn)
			p.current.Store(cur)
			j.downloaded.Add(int64(rn))
			lastData.Store(time.Now().UnixNano())
		}
		if errors.Is(rerr, io.EOF) {
			j.mu.Lock()
			defer j.mu.Unlock()
			if p.to < 0 {
				// 长度未知的盲下, 以 EOF 作为结束.
				p.to = cur - 1
				return nil
			}
			if p.completedLocked() {
				return nil
			}
			return errShortBody
		}
		if rerr != nil {
			return causeOf(ctx, rerr)
		}
	}
}

// verify 校验分片响应 (规则同 ABDM connectAndVerify): 必须是 206,
// Content-Range 起点等于请求起点, 总长度与预期一致. 返回长度不同则放行.
func (j *Job) verify(resp *http.Response, start int64) error {
	if resp.StatusCode != http.StatusPartialContent {
		return fatal(fmt.Errorf("%w: 请求起点 %d 时返回 %s", ErrRangeIgnored, start, resp.Status))
	}
	cr, ok := httpx.ParseContentRange(resp.Header.Get("Content-Range"))
	if !ok || cr.Start != start {
		return fatal(fmt.Errorf("%w: 请求起点 %d, 响应 Content-Range 为 %q",
			ErrFileChanged, start, resp.Header.Get("Content-Range")))
	}
	if cr.Total >= 0 && cr.Total != j.opts.Size {
		return fatal(fmt.Errorf("%w: 长度从 %d 变为 %d", ErrFileChanged, j.opts.Size, cr.Total))
	}
	return nil
}

// watchStall 在连接超过 StallTimeout 没有数据时取消它.
func (j *Job) watchStall(stop <-chan struct{}, lastData *atomic.Int64, cancel context.CancelCauseFunc) {
	timeout := j.opts.StallTimeout
	interval := max(min(timeout/4, time.Second), 10*time.Millisecond)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if time.Since(time.Unix(0, lastData.Load())) > timeout {
				cancel(errStalled)
				return
			}
		}
	}
}

// causeOf 把被取消的上下文错误还原成取消原因 (例如卡顿).
func causeOf(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil {
			return cause
		}
	}
	return err
}
