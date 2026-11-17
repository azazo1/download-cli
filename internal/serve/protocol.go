package serve

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Item 是浏览器扩展提交的一个下载来源.
// 字段名与 AB Download Manager 的 DownloadRequestItem 保持一致, 便于直接兼容它的扩展.
type Item struct {
	Link          string            `json:"link"`
	Headers       map[string]string `json:"headers"`
	DownloadPage  string            `json:"downloadPage"`
	SuggestedName string            `json:"suggestedName"`
	Description   string            `json:"description"`
	// Type 为 "http" 或 "hls". dn 不支持 hls.
	Type string `json:"type"`
}

// AddOptions 是扩展随下载一起提交的选项.
type AddOptions struct {
	SilentAdd   bool `json:"silentAdd"`
	SilentStart bool `json:"silentStart"`
}

// addRequest 是 /add 的请求体.
type addRequest struct {
	Items   []Item     `json:"items"`
	Options AddOptions `json:"options"`
}

// downloadTask 是 /start-headless-download 的请求体.
type downloadTask struct {
	DownloadSource Item   `json:"downloadSource"`
	Folder         string `json:"folder"`
	Name           string `json:"name"`
	QueueID        *int64 `json:"queueId"`
	CategoryID     *int64 `json:"categoryId"`
	StartDownload  bool   `json:"startDownload"`
	StartQueue     bool   `json:"startQueue"`
}

// parseAdd 解析 /add 的请求体, 同时兼容旧格式 (裸数组) 与新格式 ({"items": [...]}).
func parseAdd(body []byte) ([]Item, error) {
	var req addRequest
	if err := json.Unmarshal(body, &req); err == nil && req.Items != nil {
		return req.Items, nil
	}
	var legacy []Item
	if err := json.Unmarshal(body, &legacy); err != nil {
		return nil, fmt.Errorf("请求体不是有效的下载请求: %w", err)
	}
	return legacy, nil
}

// validateItem 检查单个下载来源是否可以用 dn 下载.
func validateItem(item Item) error {
	if strings.TrimSpace(item.Link) == "" {
		return fmt.Errorf("缺少 link")
	}
	if !strings.HasPrefix(strings.ToLower(item.Link), "http://") &&
		!strings.HasPrefix(strings.ToLower(item.Link), "https://") {
		return fmt.Errorf("不支持的链接: %s", item.Link)
	}
	if strings.EqualFold(item.Type, "hls") {
		return fmt.Errorf("dn 不支持 HLS 下载: %s", item.Link)
	}
	return nil
}
