package serve

import (
	"net/http"
	"strings"
)

// droppedHeaders 是浏览器会带上, 但 dn 必须去掉的请求头.
// 其中 Host 由 URL 决定, Content-Length 与 Accept-Encoding 由传输层决定,
// 其余是连接管理用的逐跳头, 转发给目标服务器只会带来麻烦.
var droppedHeaders = map[string]bool{
	"host":              true,
	"content-length":    true,
	"content-type":      true,
	"accept-encoding":   true,
	"connection":        true,
	"keep-alive":        true,
	"proxy-connection":  true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
	"expect":            true,
	"set-cookie":        true,
	"cookie2":           true,
}

// cleanHeaders 过滤扩展发来的请求头, 保留 Cookie, Referer, User-Agent 这类
// 真正需要的头, 去掉逐跳头与非法头.
func cleanHeaders(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for name, value := range in {
		name = strings.TrimSpace(name)
		if !validHeaderName(name) {
			continue
		}
		lower := strings.ToLower(name)
		if droppedHeaders[lower] || strings.HasPrefix(lower, "sec-") || strings.HasPrefix(lower, "proxy-") {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		// 值里不允许出现换行, 否则等于请求头注入.
		if strings.ContainsAny(value, "\r\n") {
			continue
		}
		out[name] = value
	}
	return out
}

// validHeaderName 检查名字是否是合法的 HTTP 头字段名.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}

// extensionOrigins 是允许调用本地服务的浏览器扩展来源.
var extensionOrigins = []string{
	"chrome-extension://",
	"moz-extension://",
	"safari-web-extension://",
	"safari-extension://",
}

// requestAllowed 判断请求是否来自可行的调用方.
// 只监听回环地址已经挡掉了外部主机, 这里再挡普通网页:
// 网页发起的跨站请求会带 http(s) 的 Origin, 而扩展的是 chrome-extension:// 之类.
func requestAllowed(r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		ok := false
		for _, prefix := range extensionOrigins {
			if strings.HasPrefix(origin, prefix) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if strings.EqualFold(r.Header.Get("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	return true
}
