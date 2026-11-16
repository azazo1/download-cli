// Package logging 提供基于 slog 的日志设施.
//
// 日志默认写 stderr. 进度条运行期间, 输出目标会被切换到进度条容器,
// 让日志行出现在进度条上方而不是把进度条冲乱.
package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
)

// SwitchWriter 是可在运行时切换目标的 io.Writer.
// 当前目标写入失败时 (例如进度条已结束), 自动回退到 fallback.
type SwitchWriter struct {
	mu       sync.Mutex
	target   io.Writer
	fallback io.Writer
}

// NewSwitchWriter 创建以 fallback 为初始目标的 SwitchWriter.
func NewSwitchWriter(fallback io.Writer) *SwitchWriter {
	return &SwitchWriter{target: fallback, fallback: fallback}
}

// Set 切换输出目标, nil 表示恢复 fallback.
func (w *SwitchWriter) Set(target io.Writer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if target == nil {
		target = w.fallback
	}
	w.target = target
}

// Write 实现 io.Writer.
func (w *SwitchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	target := w.target
	w.mu.Unlock()
	n, err := target.Write(p)
	if err != nil && target != w.fallback {
		return w.fallback.Write(p)
	}
	return n, err
}

// Options 是日志初始化参数.
type Options struct {
	Level string
	// Console 是终端日志的输出目标.
	Console io.Writer
	// File 是可选的日志文件, 总是以 debug 级别写入.
	File io.Writer
}

// New 构造日志器.
func New(opts Options) *slog.Logger {
	console := opts.Console
	if console == nil {
		console = os.Stderr
	}
	level := ParseLevel(opts.Level)
	consoleHandler := NewConsoleHandler(console, level)
	if opts.File == nil {
		return slog.New(consoleHandler)
	}
	fileHandler := slog.NewTextHandler(opts.File, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(slog.NewMultiHandler(consoleHandler, fileHandler))
}

// ParseLevel 把级别文本解析为 slog 级别, 无法识别时回落到 info.
func ParseLevel(text string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "debug", "trace":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Discard 返回丢弃全部输出的日志器, 供测试使用.
func Discard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
