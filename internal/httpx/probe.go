package httpx

import (
	"context"
	"net/http"
	"strings"
)

// ProbeResult 是下载前探测得到的资源信息.
type ProbeResult struct {
	// FinalURL 是跟随重定向后的地址, 分片请求都发往它.
	FinalURL string
	// Size 为 -1 表示长度未知.
	Size int64
	// Resumable 表示服务器支持 Range, 可以多连接与续传.
	Resumable    bool
	ETag         string
	LastModified string
	ContentType  string
	// FileName 来自 Content-Disposition 或 URL, 可能为空.
	FileName string
}

// IsHTML 表示响应是网页, 常见原因是链接失效或需要登录.
func (p ProbeResult) IsHTML() bool {
	return strings.Contains(strings.ToLower(p.ContentType), "text/html")
}

// Probe 探测资源. 先请求 bytes=0-255, 满足条件才判定支持 Range;
// 否则不带 Range 再请求一次, 只读响应头.
func (c *Client) Probe(ctx context.Context, rawURL string) (ProbeResult, error) {
	resp, err := c.Get(ctx, rawURL, 0, 255)
	if err == nil {
		resp.Body.Close()
		if r, ok := rangedResult(resp); ok {
			return r, nil
		}
		if resp.StatusCode == http.StatusOK {
			return plainResult(resp), nil
		}
	} else if ctx.Err() != nil {
		return ProbeResult{}, ctx.Err()
	}
	// 走到这里说明 Range 请求失败 (416, 连接被重置等) 或响应不规范, 换成普通请求再试.
	resp, err = c.Get(ctx, rawURL, -1, -1)
	if err != nil {
		return ProbeResult{}, err
	}
	resp.Body.Close()
	return plainResult(resp), nil
}

func rangedResult(resp *http.Response) (ProbeResult, bool) {
	if resp.StatusCode != http.StatusPartialContent || resp.ContentLength < 0 {
		return ProbeResult{}, false
	}
	cr, ok := ParseContentRange(resp.Header.Get("Content-Range"))
	if !ok || cr.Total < 0 || cr.Start != 0 {
		return ProbeResult{}, false
	}
	r := baseResult(resp)
	r.Size = cr.Total
	r.Resumable = true
	return r, true
}

func plainResult(resp *http.Response) ProbeResult {
	r := baseResult(resp)
	r.Size = resp.ContentLength
	if r.Size < 0 {
		r.Size = -1
	}
	return r
}

func baseResult(resp *http.Response) ProbeResult {
	finalURL := resp.Request.URL.String()
	name := ParseContentDisposition(resp.Header.Get("Content-Disposition"))
	if name == "" {
		name = NameFromURL(finalURL)
	}
	return ProbeResult{
		FinalURL:     finalURL,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
		ContentType:  resp.Header.Get("Content-Type"),
		FileName:     name,
	}
}
