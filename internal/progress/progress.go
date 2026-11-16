// Package progress 显示下载进度: 终端里画进度条, 非终端环境定时输出进度日志.
package progress

import (
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"

	"github.com/azazo1/download-cli/internal/logging"
)

// Source 提供进度数据.
type Source interface {
	Downloaded() int64
	Active() int
}

// Options 是进度展示参数.
type Options struct {
	Name string
	// Total 为 -1 表示长度未知.
	Total int64
	// Bar 为 true 时画进度条, 否则输出进度日志.
	Bar bool
	// Out 是进度条输出目标, 通常为 stderr.
	Out io.Writer
	// LogSwitch 用于在进度条期间把日志转到进度条上方.
	LogSwitch *logging.SwitchWriter
	Logger    *slog.Logger
	// LogInterval 是非终端模式下的进度日志间隔.
	LogInterval time.Duration
}

// Display 是一个运行中的进度展示.
type Display struct {
	stop chan struct{}
	done sync.WaitGroup
	fin  func(success bool)
}

// Start 开始展示进度.
func Start(src Source, opts Options) *Display {
	d := &Display{stop: make(chan struct{})}
	if opts.Bar {
		d.startBar(src, opts)
	} else {
		d.startLog(src, opts)
	}
	return d
}

// Stop 结束展示. success 为 false 时进度条显示为中止.
func (d *Display) Stop(success bool) {
	close(d.stop)
	d.done.Wait()
	d.fin(success)
}

func (d *Display) startBar(src Source, opts Options) {
	p := mpb.New(mpb.WithOutput(opts.Out), mpb.WithWidth(80), mpb.WithRefreshRate(150*time.Millisecond))
	unknown := opts.Total < 0
	total := max(opts.Total, 0)
	bar := p.AddBar(total,
		mpb.PrependDecorators(
			decor.Name(truncate(opts.Name, 32), decor.WCSyncSpaceR),
			decor.Any(func(decor.Statistics) string { return fmt.Sprintf("%d 连接", src.Active()) }, decor.WCSyncSpace),
		),
		mpb.AppendDecorators(
			decor.CountersKibiByte("% .1f / % .1f", decor.WCSyncSpace),
			decor.EwmaSpeed(decor.SizeB1024(0), "% .1f", 30, decor.WCSyncSpace),
			decor.OnComplete(decor.EwmaETA(decor.ET_STYLE_GO, 30, decor.WCSyncSpace), "完成"),
		),
	)
	if opts.LogSwitch != nil {
		opts.LogSwitch.Set(p)
	}
	// 续传时已有的数据不计入速度.
	start := src.Downloaded()
	bar.SetCurrent(start)
	bar.DecoratorAverageAdjust(time.Now())

	d.done.Add(1)
	go func() {
		defer d.done.Done()
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		last := time.Now()
		for {
			select {
			case <-d.stop:
				return
			case now := <-t.C:
				cur := src.Downloaded()
				if unknown {
					bar.SetTotal(cur+1, false)
				}
				bar.EwmaSetCurrent(cur, now.Sub(last))
				last = now
			}
		}
	}()
	d.fin = func(success bool) {
		if success {
			cur := src.Downloaded()
			if !unknown {
				cur = total
			}
			bar.SetTotal(cur, true)
		} else {
			bar.Abort(false)
		}
		p.Wait()
		if opts.LogSwitch != nil {
			opts.LogSwitch.Set(nil)
		}
	}
}

func (d *Display) startLog(src Source, opts Options) {
	interval := opts.LogInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	d.done.Add(1)
	go func() {
		defer d.done.Done()
		t := time.NewTicker(interval)
		defer t.Stop()
		prev := src.Downloaded()
		for {
			select {
			case <-d.stop:
				return
			case <-t.C:
				cur := src.Downloaded()
				speed := float64(cur-prev) / interval.Seconds()
				prev = cur
				args := []any{
					"文件", opts.Name,
					"已下载", humanize.IBytes(uint64(cur)),
					"速度", humanize.IBytes(uint64(speed)) + "/s",
					"连接", src.Active(),
				}
				if opts.Total > 0 {
					args = append(args, "进度", fmt.Sprintf("%.1f%%", float64(cur)*100/float64(opts.Total)))
				}
				opts.Logger.Info("下载进度", args...)
			}
		}
	}()
	d.fin = func(bool) {}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
