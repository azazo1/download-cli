package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// levelWidth 是级别字段的固定宽度, 保证后续内容对齐.
const levelWidth = 4

// consoleHandler 输出人类友好的单行日志: "22:31:02 INFO  开始下载 链接=...".
// time 属性被渲染为行首的时间, level 属性渲染为定宽大写级别.
type consoleHandler struct {
	mu    *sync.Mutex
	out   io.Writer
	level slog.Level
	attrs []slog.Attr
}

// NewConsoleHandler 创建一个简洁的终端日志 handler.
func NewConsoleHandler(out io.Writer, level slog.Level) slog.Handler {
	return &consoleHandler{mu: &sync.Mutex{}, out: out, level: level}
}

func (h *consoleHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &consoleHandler{mu: h.mu, out: h.out, level: h.level}
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return next
}

func (h *consoleHandler) WithGroup(string) slog.Handler { return h }

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format("15:04:05"))
	b.WriteByte(' ')
	level := r.Level.String()
	if r.Level < slog.LevelInfo {
		level = strings.ToLower(level)
	}
	b.WriteString(fmt.Sprintf("%-*s ", levelWidth, strings.ToUpper(level)))
	b.WriteString(r.Message)
	var appendAttr func(a slog.Attr)
	appendAttr = func(a slog.Attr) {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			for _, ga := range v.Group() {
				appendAttr(ga)
			}
			return
		}
		if v.Kind() == slog.KindString && v.String() == "" {
			return
		}
		b.WriteByte(' ')
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(fmtValue(v))
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == slog.TimeKey || a.Key == slog.LevelKey || a.Key == slog.MessageKey {
			return true
		}
		appendAttr(a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

// fmtValue 把属性值渲染成终端里易读的形式, 含空格的字符串加引号.
func fmtValue(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if strings.ContainsAny(s, " \t") {
			return strconv.Quote(s)
		}
		return s
	case slog.KindTime:
		return v.Time().Format("15:04:05")
	case slog.KindDuration:
		return v.Duration().Round(time.Millisecond).String()
	default:
		return v.String()
	}
}
