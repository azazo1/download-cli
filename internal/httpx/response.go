package httpx

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StatusError 表示非 2xx 响应.
type StatusError struct {
	Code   int
	Status string
	// RetryAfter 来自 Retry-After 头, 没有时为 0.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("服务器返回 %s", e.Status) }

// Hint 返回针对该状态码的可操作提示, 没有针对性提示时返回空串.
func (e *StatusError) Hint() string {
	switch e.Code {
	case http.StatusUnauthorized:
		return "需要登录, 可以用 -H 传入 Cookie 或 Authorization 头"
	case http.StatusForbidden:
		return "服务器拒绝了请求, 可以用 --user-agent 换其它 User-Agent, 或校验 -H 传入的 Cookie 和 Referer"
	case http.StatusNotFound:
		return "链接不存在或已失效, 检查地址是否正确"
	case http.StatusProxyAuthRequired:
		return "代理需要认证, 把用户名和密码写进 --proxy 的地址里"
	case http.StatusTooManyRequests:
		return "请求过于频繁, 稍后重试或减少 --connections"
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return "服务器暂时不可用, 稍后重试"
	}
	return ""
}

func newStatusError(resp *http.Response) *StatusError {
	e := &StatusError{Code: resp.StatusCode, Status: resp.Status}
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			e.RetryAfter = max(time.Until(t), 0)
		}
	}
	return e
}

// ContentRange 是解析后的 Content-Range 头. Total 为 -1 表示总长度未知 (*).
type ContentRange struct {
	Start, End, Total int64
}

// ParseContentRange 解析 "bytes start-end/total".
func ParseContentRange(v string) (ContentRange, bool) {
	v = strings.TrimSpace(v)
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return ContentRange{}, false
	}
	span, total, ok := strings.Cut(strings.TrimSpace(rest), "/")
	if !ok {
		return ContentRange{}, false
	}
	s, e, ok := strings.Cut(span, "-")
	if !ok {
		return ContentRange{}, false
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(e), 10, 64)
	if err1 != nil || err2 != nil || start < 0 || end < start {
		return ContentRange{}, false
	}
	cr := ContentRange{Start: start, End: end, Total: -1}
	if t := strings.TrimSpace(total); t != "*" {
		n, err := strconv.ParseInt(t, 10, 64)
		if err != nil || n <= end {
			return ContentRange{}, false
		}
		cr.Total = n
	}
	return cr, true
}

var looseFilename = regexp.MustCompile(`(?i)filename\s*=\s*"?([^";]+)"?`)

// ParseContentDisposition 取出文件名, filename* 优先于 filename. 取不到时返回空串.
func ParseContentDisposition(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	// mime.ParseMediaType 会解码 RFC 5987 的 filename*, 并以 filename 键返回.
	if _, params, err := mime.ParseMediaType(v); err == nil {
		if name := params["filename"]; name != "" {
			return name
		}
	}
	// 非标准头 (例如未加引号且包含空格) 的宽松回退.
	if m := looseFilename.FindStringSubmatch(v); m != nil {
		name := strings.TrimSpace(m[1])
		if decoded, err := url.PathUnescape(name); err == nil {
			name = decoded
		}
		return name
	}
	return ""
}

// NameFromURL 返回 URL 路径的最后一段 (已解码), 取不到时返回空串.
func NameFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if decoded, err := url.PathUnescape(p); err == nil {
		p = decoded
	}
	return p
}
