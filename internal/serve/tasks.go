package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/azazo1/download-cli/internal/progress"
	"github.com/azazo1/download-cli/internal/task"
)

// Status 是一个任务的运行状态.
type Status int

const (
	StatusDownloading Status = iota
	StatusPaused
	StatusDone
	StatusFailed
)

// String 返回状态的中文名, 供界面直接显示.
func (s Status) String() string {
	switch s {
	case StatusDownloading:
		return "下载中"
	case StatusPaused:
		return "已暂停"
	case StatusDone:
		return "已完成"
	case StatusFailed:
		return "失败"
	default:
		return "未知"
	}
}

// Snapshot 是任务在某一时刻的只读快照, 供界面轮询.
type Snapshot struct {
	ID         int
	Name       string
	Link       string
	Status     Status
	Downloaded int64
	Total      int64
	Active     int
	Error      string
	Path       string
}

// task 是一个下载任务的可变状态. 所有字段由 mu 保护.
type taskState struct {
	id   int
	item Item
	name string
	// dir 非空时覆盖全局下载目录, 供 /start-headless-download 的 folder 使用.
	dir string

	mu      sync.Mutex
	status  Status
	source  progress.Source
	total   int64
	done    int64
	active  int
	errText string
	path    string
	// wantPause 区分 "用户要求暂停" 与 "进程正在退出".
	wantPause bool
	cancel    context.CancelFunc
}

// submit 创建任务并立即开始下载. dn 不做排队, 任务一到达就并发跑.
func (s *Server) submit(item Item, dir string) *taskState {
	s.mu.Lock()
	s.nextID++
	t := &taskState{id: s.nextID, item: item, name: displayName(item), dir: dir, status: StatusDownloading}
	s.tasks = append(s.tasks, t)
	s.mu.Unlock()
	s.start(t)
	return t
}

// start 为任务创建独立的 context 并在后台执行.
func (s *Server) start(t *taskState) {
	ctx, cancel := context.WithCancel(s.ctx)
	t.mu.Lock()
	t.status = StatusDownloading
	t.wantPause = false
	t.source = nil
	t.errText = ""
	t.done = 0
	t.active = 0
	t.cancel = cancel
	t.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx, t)
		cancel()
	}()
}

// run 执行一次下载, 结束后把结果写回任务状态.
func (s *Server) run(ctx context.Context, t *taskState) {
	opts := s.opts.Task
	opts.URL = t.item.Link
	if name := strings.TrimSpace(t.item.SuggestedName); name != "" {
		opts.Output = name
	}
	if t.dir != "" {
		opts.Dir = t.dir
	}
	opts.ShowBar = false
	opts.ProgressOut = io.Discard
	opts.LogSwitch = nil
	opts.OnStart = func(source progress.Source, size int64) {
		t.mu.Lock()
		t.source = source
		t.total = size
		t.mu.Unlock()
	}

	log := s.log.With("任务", t.id)
	client := s.base.Derive(cleanHeaders(t.item.Headers))
	res, err := task.Run(ctx, client, log, opts)

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.source != nil {
		t.done = t.source.Downloaded()
		t.source = nil
	}
	switch {
	case err == nil:
		t.status = StatusDone
		t.path = res.Path
		t.total = res.Size
		t.done = res.Size
	case errors.Is(err, task.ErrInterrupted):
		t.status = StatusPaused
		if t.wantPause {
			log.Info("任务已暂停")
		} else {
			log.Info("任务已停止")
		}
	default:
		t.status = StatusFailed
		t.errText = err.Error()
		log.Error("任务失败", "错误", err)
	}
}

// Pause 暂停一个正在下载的任务. 进度会写进 .part.json, 恢复时接着下.
func (s *Server) Pause(id int) error {
	t := s.find(id)
	if t == nil {
		return fmt.Errorf("任务 %d 不存在", id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.status != StatusDownloading {
		return fmt.Errorf("任务 %d 当前状态是 %s, 不能暂停", id, t.status)
	}
	t.wantPause = true
	t.cancel()
	return nil
}

// Resume 恢复一个已暂停或失败的任务.
func (s *Server) Resume(id int) error {
	t := s.find(id)
	if t == nil {
		return fmt.Errorf("任务 %d 不存在", id)
	}
	t.mu.Lock()
	status := t.status
	t.mu.Unlock()
	switch status {
	case StatusPaused, StatusFailed:
		s.start(t)
		return nil
	case StatusDownloading:
		if t.pausing() {
			return fmt.Errorf("任务 %d 正在暂停, 请稍后再恢复", id)
		}
		return fmt.Errorf("任务 %d 正在下载", id)
	default:
		return fmt.Errorf("任务 %d 已完成, 不需要恢复", id)
	}
}

// Remove 把任务从列表里移除. 已经下载的数据文件不会被删除.
func (s *Server) Remove(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.tasks {
		if t.id != id {
			continue
		}
		t.mu.Lock()
		status := t.status
		t.mu.Unlock()
		if status == StatusDownloading {
			return fmt.Errorf("任务 %d 正在下载, 请先暂停", id)
		}
		s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
		return nil
	}
	return fmt.Errorf("任务 %d 不存在", id)
}

// pausing 表示任务已被要求暂停, 但下载循环还没退出.
func (t *taskState) pausing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status == StatusDownloading && t.wantPause
}

// Snapshots 返回全部任务的快照, 下载中的任务会读取实时进度.
func (s *Server) Snapshots() []Snapshot {
	s.mu.Lock()
	tasks := make([]*taskState, len(s.tasks))
	copy(tasks, s.tasks)
	s.mu.Unlock()

	out := make([]Snapshot, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, t.snapshot())
	}
	return out
}

func (t *taskState) snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	snap := Snapshot{
		ID: t.id, Name: t.name, Link: t.item.Link, Status: t.status,
		Downloaded: t.done, Total: t.total, Active: t.active,
		Error: t.errText, Path: t.path,
	}
	if t.status == StatusDownloading && t.source != nil {
		snap.Downloaded = t.source.Downloaded()
		snap.Active = t.source.Active()
	}
	return snap
}

// find 按 id 查找任务.
func (s *Server) find(id int) *taskState {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.id == id {
			return t
		}
	}
	return nil
}

// displayName 给任务起一个用于界面的名字.
func displayName(item Item) string {
	if name := strings.TrimSpace(item.SuggestedName); name != "" {
		return name
	}
	if u, err := url.Parse(item.Link); err == nil {
		base := path.Base(u.EscapedPath())
		if unescaped, err := url.PathUnescape(base); err == nil {
			base = unescaped
		}
		if base != "" && base != "." && base != "/" {
			return base
		}
		if u.Host != "" {
			return u.Host
		}
	}
	return item.Link
}
