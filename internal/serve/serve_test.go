package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseAddSupportsBothFormats(t *testing.T) {
	items, err := parseAdd([]byte(`{"items":[{"link":"https://example.com/a.zip","headers":{"Cookie":"a=b"}}],"options":{"silentAdd":true}}`))
	if err != nil {
		t.Fatalf("解析新格式失败: %v", err)
	}
	if len(items) != 1 || items[0].Link != "https://example.com/a.zip" || items[0].Headers["Cookie"] != "a=b" {
		t.Fatalf("新格式解析结果不对: %+v", items)
	}

	items, err = parseAdd([]byte(`[{"link":"https://example.com/b.zip"}]`))
	if err != nil {
		t.Fatalf("解析旧格式失败: %v", err)
	}
	if len(items) != 1 || items[0].Link != "https://example.com/b.zip" {
		t.Fatalf("旧格式解析结果不对: %+v", items)
	}

	if _, err := parseAdd([]byte(`not json`)); err == nil {
		t.Fatal("非法 JSON 应该报错")
	}
}

func TestCleanHeaders(t *testing.T) {
	got := cleanHeaders(map[string]string{
		"Cookie":          "sid=abc",
		"Referer":         "https://example.com/a",
		"User-Agent":      "Mozilla/5.0",
		"Host":            "example.com",
		"Content-Length":  "10",
		"Accept-Encoding": "gzip",
		"Sec-Fetch-Site":  "cross-site",
		"Bad Name":        "x",
		"Empty":           "",
	})
	want := map[string]string{
		"Cookie":     "sid=abc",
		"Referer":    "https://example.com/a",
		"User-Agent": "Mozilla/5.0",
	}
	if len(got) != len(want) {
		t.Fatalf("过滤后剩下 %d 个头, 期望 %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("头 %s = %q, 期望 %q", k, got[k], v)
		}
	}
}

func TestRequestAllowed(t *testing.T) {
	cases := []struct {
		name    string
		origin  string
		fetch   string
		allowed bool
	}{
		{"无 Origin", "", "", true},
		{"扩展来源", "chrome-extension://abcdef", "", true},
		{"火狐扩展来源", "moz-extension://abcdef", "", true},
		{"网页来源", "https://evil.example", "", false},
		{"跨站抓取", "", "cross-site", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/add", nil)
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.fetch != "" {
				r.Header.Set("Sec-Fetch-Site", c.fetch)
			}
			if got := requestAllowed(r); got != c.allowed {
				t.Fatalf("requestAllowed = %v, 期望 %v", got, c.allowed)
			}
		})
	}
}

func TestAuthorized(t *testing.T) {
	s := &Server{opts: Options{APIKey: "secret", EnforceAPIKey: true}}
	withKey := httptest.NewRequest(http.MethodPost, "/add", nil)
	withKey.Header.Set("X-Api-Key", "secret")
	without := httptest.NewRequest(http.MethodPost, "/add", nil)
	// /add 与 /ping 在强制开启时必须带 key, 无界面下载端点始终要求 key.
	if s.authorized(without, false) {
		t.Fatal("强制开启时缺少 key 应被拒绝")
	}
	if !s.authorized(withKey, false) || !s.authorized(withKey, true) {
		t.Fatal("带正确 key 应通过")
	}

	// 未配置 key 时, 浏览器端点放行, 无界面下载端点拒绝.
	loose := &Server{opts: Options{}}
	if !loose.authorized(without, false) {
		t.Fatal("未配置 key 时浏览器端点应放行")
	}
	if loose.authorized(without, true) {
		t.Fatal("未配置 key 时无界面下载端点应拒绝")
	}
}
