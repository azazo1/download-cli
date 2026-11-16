// Package httpx 封装下载用到的 HTTP 请求: 公共请求头, 代理, Range 请求与探测.
package httpx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent 是未配置 User-Agent 时使用的值.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"

// Options 是客户端参数.
type Options struct {
	UserAgent string
	// Proxy 为空时读取 HTTP_PROXY / HTTPS_PROXY 环境变量.
	Proxy   string
	Headers map[string]string
	// ResponseHeaderTimeout 是等待响应头的上限.
	ResponseHeaderTimeout time.Duration
	// Transport 仅供测试替换.
	Transport http.RoundTripper
}

// Client 是带公共请求头的 HTTP 客户端.
type Client struct {
	http      *http.Client
	userAgent string
	headers   map[string]string
}

// NewClient 根据参数构造客户端.
func NewClient(opts Options) (*Client, error) {
	rt := opts.Transport
	if rt == nil {
		proxy := http.ProxyFromEnvironment
		if strings.TrimSpace(opts.Proxy) != "" {
			u, err := url.Parse(opts.Proxy)
			if err != nil || u.Host == "" {
				return nil, fmt.Errorf("代理地址 %q 无效", opts.Proxy)
			}
			proxy = http.ProxyURL(u)
		}
		timeout := opts.ResponseHeaderTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		rt = &http.Transport{
			Proxy:                 proxy,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: timeout,
			// 分片下载按字节偏移写入, 必须拿到原始字节流.
			DisableCompression: true,
		}
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	headers := make(map[string]string, len(opts.Headers))
	for k, v := range opts.Headers {
		headers[k] = v
	}
	return &Client{http: &http.Client{Transport: rt}, userAgent: ua, headers: headers}, nil
}

// Get 发出 GET 请求. start < 0 时不带 Range; end < 0 时表示到文件末尾.
// 返回的响应若非 2xx, 会关闭 body 并返回 *StatusError.
func (c *Client) Get(ctx context.Context, rawURL string, start, end int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	req.Header.Set("Accept-Encoding", "identity")
	if start >= 0 {
		rng := "bytes=" + strconv.FormatInt(start, 10) + "-"
		if end >= 0 {
			rng += strconv.FormatInt(end, 10)
		}
		req.Header.Set("Range", rng)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, newStatusError(resp)
	}
	return resp, nil
}

// HeaderNames 返回附加请求头的名字 (已排序), 用于日志 (不输出值).
func (c *Client) HeaderNames() []string {
	names := make([]string, 0, len(c.headers))
	for k := range c.headers {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}
