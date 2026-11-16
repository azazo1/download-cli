package engine

import (
	"errors"
	"net/http"
	"time"

	"github.com/azazo1/download-cli/internal/httpx"
)

var (
	// ErrFileChanged 表示服务器上的文件与续传记录不一致, 需要从头下载.
	ErrFileChanged = errors.New("服务器上的文件已变化")
	// ErrRangeIgnored 表示多分片下载过程中服务器不再接受 Range.
	ErrRangeIgnored = errors.New("服务器不再支持 Range 请求")

	errStalled   = errors.New("连接长时间没有收到数据")
	errShortBody = errors.New("服务器提前结束了响应")
)

// fatalError 包装不可重试的错误.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

func fatal(err error) error { return &fatalError{err: err} }

// isFatal 判断错误是否应当中止整个任务.
func isFatal(err error) bool {
	var fe *fatalError
	if errors.As(err, &fe) {
		return true
	}
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.Code >= 400 && se.Code < 500 &&
			se.Code != http.StatusRequestTimeout && se.Code != http.StatusTooManyRequests
	}
	return false
}

// isThrottled 判断错误是否为服务器限流 (429 / 503).
func isThrottled(err error) bool {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code == http.StatusServiceUnavailable
	}
	return false
}

// retryAfter 返回服务器建议的重试等待时间, 没有时为 0.
func retryAfter(err error) time.Duration {
	var se *httpx.StatusError
	if errors.As(err, &se) {
		return se.RetryAfter
	}
	return 0
}
