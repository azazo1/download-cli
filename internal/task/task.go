// Package task 编排单个 URL 的下载流程: 探测, 命名, 续传判定, 下载与收尾.
package task

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/azazo1/download-cli/internal/engine"
	"github.com/azazo1/download-cli/internal/httpx"
	"github.com/azazo1/download-cli/internal/logging"
	"github.com/azazo1/download-cli/internal/output"
	"github.com/azazo1/download-cli/internal/progress"
	"github.com/azazo1/download-cli/internal/state"
)

// ErrInterrupted 表示下载被用户中断, 状态已保存.
var ErrInterrupted = errors.New("下载已暂停")

// Options 是一次下载的参数.
type Options struct {
	URL          string
	Dir          string
	Output       string
	Connections  int
	MinPartSize  int64
	DynamicSplit bool
	Retries      int
	StallTimeout time.Duration
	Overwrite    bool
	Restart      bool
	// ShowBar 为 true 时画进度条.
	ShowBar     bool
	ProgressOut io.Writer
	LogSwitch   *logging.SwitchWriter
	// OnStart 在探测完成, 开始传输后调用, 提供实时进度源与文件总大小 (未知时为 -1).
	// 调用方不应在 Run 返回后继续读取 source.
	OnStart func(source progress.Source, size int64)
}

// Result 是一次成功下载的结果.
type Result struct {
	Path string
	Size int64
}

// Run 下载一个 URL.
func Run(ctx context.Context, client *httpx.Client, log *slog.Logger, opts Options) (Result, error) {
	begin := time.Now()
	log.Info("开始下载", "链接", opts.URL, "目录", opts.Dir, "连接数", opts.Connections)

	probe, err := client.Probe(ctx, opts.URL)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ErrInterrupted
		}
		return Result{}, fmt.Errorf("探测链接失败: %w", withHint(err))
	}
	log.Info("探测完成", "文件名", probe.FileName, "大小", sizeText(probe.Size),
		"支持分片", probe.Resumable, "耗时", time.Since(begin).Round(time.Millisecond))

	name := opts.Output
	if name == "" {
		name = probe.FileName
	}
	name = output.SanitizeName(name)
	if probe.IsHTML() && filepath.Ext(name) != ".html" && filepath.Ext(name) != ".htm" {
		log.Warn("服务器返回的是网页, 链接可能已失效或需要登录", "Content-Type", probe.ContentType)
	}

	target, prev, err := pickTarget(opts, name, log)
	if err != nil {
		return Result{}, err
	}
	resume := prev != nil && canResume(*prev, probe, target, log)

	var parts []engine.PartState
	if resume {
		parts = prev.Parts
		done := prev.Downloaded()
		log.Info("继续之前的下载", "文件", target.Final,
			"已完成", fmt.Sprintf("%.1f%%", float64(done)*100/float64(max(prev.Size, 1))))
	}

	f, err := output.OpenPart(target.PartPath(), probe.Size, !resume)
	if err != nil {
		return Result{}, err
	}
	if probe.Size == 0 {
		if err := output.Finalize(f, target, 0); err != nil {
			return Result{}, err
		}
		_ = state.Remove(target.StatePath())
		log.Info("下载完成 (空文件)", "文件", target.Final)
		return Result{Path: target.Final}, nil
	}

	st := state.State{
		URL: opts.URL, FinalURL: probe.FinalURL, Size: probe.Size,
		ETag: probe.ETag, LastModified: probe.LastModified, Resumable: probe.Resumable,
	}
	job := engine.NewJob(client, engine.Options{
		URL: probe.FinalURL, Size: probe.Size, Resumable: probe.Resumable, File: f, Parts: parts,
		Connections: opts.Connections, MinPartSize: opts.MinPartSize, DynamicSplit: opts.DynamicSplit,
		Retries: opts.Retries, StallTimeout: opts.StallTimeout,
		Save: func(ps []engine.PartState) error {
			s := st
			s.Parts = ps
			return state.Save(target.StatePath(), s)
		},
		Logger: log,
	})
	if !resume {
		log.Info("初始分片", "数量", job.PartCount(), "最小分片", humanize.IBytes(uint64(opts.MinPartSize)))
	}
	if opts.OnStart != nil {
		opts.OnStart(job, probe.Size)
	}

	display := progress.Start(job, progress.Options{
		Name: target.Name(), Total: probe.Size, Bar: opts.ShowBar,
		Out: opts.ProgressOut, LogSwitch: opts.LogSwitch, Logger: log,
	})
	runStart := time.Now()
	runErr := job.Run(ctx)
	display.Stop(runErr == nil)

	if runErr != nil {
		f.Close()
		if errors.Is(runErr, context.Canceled) {
			log.Info("已暂停, 重新运行同一命令即可继续", "文件", target.Final,
				"已下载", humanize.IBytes(uint64(job.Downloaded())))
			return Result{}, ErrInterrupted
		}
		return Result{}, fmt.Errorf("下载 %s 失败 (进度已保存, 可重新运行续传): %w", target.Name(), withHint(runErr))
	}

	size := probe.Size
	if size < 0 {
		size = job.Downloaded()
	}
	if err := output.Finalize(f, target, size); err != nil {
		return Result{}, err
	}
	if err := state.Remove(target.StatePath()); err != nil {
		log.Warn("删除续传状态文件失败", "错误", err)
	}
	stats := job.Stats()
	elapsed := time.Since(runStart)
	fresh := size - stats.Reused
	speed := float64(fresh) / max(elapsed.Seconds(), 0.001)
	log.Info("下载完成", "文件", target.Final, "大小", humanize.IBytes(uint64(size)),
		"耗时", elapsed.Round(time.Millisecond), "平均速度", humanize.IBytes(uint64(speed))+"/s",
		"分裂次数", stats.Splits, "重试次数", stats.Retries)
	return Result{Path: target.Final, Size: size}, nil
}

// pickTarget 决定目标路径. 同名位置存在同一 URL 的续传状态时沿用它, 否则按重名策略生成新路径.
func pickTarget(opts Options, name string, log *slog.Logger) (output.Target, *state.State, error) {
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return output.Target{}, nil, fmt.Errorf("创建目录 %s 失败: %w", opts.Dir, err)
	}
	candidate := output.Target{Final: filepath.Join(opts.Dir, name)}
	prev, err := state.Load(candidate.StatePath())
	switch {
	case err == nil && prev.URL == opts.URL:
		if opts.Restart {
			log.Info("按 --restart 要求从头下载", "文件", candidate.Final)
			return candidate, nil, nil
		}
		return candidate, &prev, nil
	case err != nil && !errors.Is(err, state.ErrNotFound):
		log.Warn("续传状态不可用, 将从头下载", "原因", err)
		return candidate, nil, nil
	}
	t, err := output.Resolve(opts.Dir, name, opts.Overwrite)
	return t, nil, err
}

// canResume 检查续传条件, 不满足时输出原因.
func canResume(prev state.State, probe httpx.ProbeResult, t output.Target, log *slog.Logger) bool {
	reason := ""
	switch {
	case !prev.Resumable || !probe.Resumable:
		reason = "服务器不支持分片续传"
	case prev.Size != probe.Size:
		reason = fmt.Sprintf("文件大小从 %d 变为 %d", prev.Size, probe.Size)
	case prev.ETag != "" && probe.ETag != "" && prev.ETag != probe.ETag:
		reason = "ETag 已变化"
	case output.PartSize(t.PartPath()) != prev.Size:
		reason = "未完成的数据文件缺失或大小不对"
	case len(prev.Parts) == 0:
		reason = "续传状态中没有分片"
	}
	if reason != "" {
		log.Warn("无法续传, 将从头下载", "原因", reason)
		return false
	}
	return true
}

func sizeText(n int64) string {
	if n < 0 {
		return "未知"
	}
	return humanize.IBytes(uint64(n))
}

// withHint 在 HTTP 状态错误后附加可操作提示.
func withHint(err error) error {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		if hint := se.Hint(); hint != "" {
			return fmt.Errorf("%w (%s)", err, hint)
		}
	}
	return err
}
