package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azazo1/download-cli/internal/httpx"
)

// testServer 用 http.ServeContent 提供随机数据, 并可按请求起点限速, 忽略 Range 或卡住连接.
type testServer struct {
	data []byte
	// delay 返回每写 32KiB 前的等待时间, 参数为请求起点.
	delay       func(start int64) time.Duration
	ignoreRange bool
	// hangOnce 为 true 时, 第一个起点为 0 的请求只写响应头然后卡住.
	hangOnce atomic.Bool

	mu     sync.Mutex
	starts []int64
}

func newTestServer(t *testing.T, size int) (*testServer, *httptest.Server) {
	t.Helper()
	ts := &testServer{data: make([]byte, size)}
	if _, err := rand.Read(ts.data); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ts)
	t.Cleanup(srv.Close)
	return ts, srv
}

func rangeStart(r *http.Request) int64 {
	v, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes=")
	if !ok {
		return -1
	}
	s, _, _ := strings.Cut(v, "-")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func (ts *testServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := rangeStart(r)
	ts.mu.Lock()
	ts.starts = append(ts.starts, start)
	ts.mu.Unlock()
	if ts.ignoreRange {
		r.Header.Del("Range")
		start = 0
	}
	if start == 0 && ts.hangOnce.CompareAndSwap(true, false) {
		w.Header().Set("Content-Range", "bytes 0-"+strconv.Itoa(len(ts.data)-1)+"/"+strconv.Itoa(len(ts.data)))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return
	}
	var d time.Duration
	if ts.delay != nil {
		d = ts.delay(max(start, 0))
	}
	http.ServeContent(&slowWriter{ResponseWriter: w, delay: d}, r, "data.bin", time.Time{}, bytes.NewReader(ts.data))
}

func (ts *testServer) requestStarts() []int64 {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]int64(nil), ts.starts...)
}

type slowWriter struct {
	http.ResponseWriter
	delay time.Duration
}

func (w *slowWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), 32*1024)]
		if w.delay > 0 {
			time.Sleep(w.delay)
		}
		m, err := w.ResponseWriter.Write(chunk)
		n += m
		if err != nil {
			return n, err
		}
		p = p[len(chunk):]
	}
	return n, nil
}

type harness struct {
	t      *testing.T
	client *httpx.Client
	url    string
	path   string
	saved  []PartState
	mu     sync.Mutex
}

func newHarness(t *testing.T, url string) *harness {
	c, err := httpx.NewClient(httpx.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, client: c, url: url, path: filepath.Join(t.TempDir(), "out.bin")}
}

func (h *harness) job(size int64, resumable bool, parts []PartState, mod func(*Options)) (*Job, *os.File) {
	f, err := os.OpenFile(h.path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { f.Close() })
	if err := f.Truncate(size); err != nil {
		h.t.Fatal(err)
	}
	opts := Options{
		URL: h.url, Size: size, Resumable: resumable, File: f, Parts: parts,
		Connections: 4, MinPartSize: 256 * 1024, DynamicSplit: true, Retries: 3,
		StallTimeout: 5 * time.Second, RetryBaseDelay: 10 * time.Millisecond, SaveInterval: 50 * time.Millisecond,
		Save: func(s []PartState) error {
			h.mu.Lock()
			h.saved = s
			h.mu.Unlock()
			return nil
		},
	}
	if mod != nil {
		mod(&opts)
	}
	return NewJob(h.client, opts), f
}

func (h *harness) assertSame(want []byte) {
	h.t.Helper()
	got, err := os.ReadFile(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	if sha256.Sum256(got) != sha256.Sum256(want) {
		h.t.Fatalf("下载内容与源数据不一致 (长度 %d / %d)", len(got), len(want))
	}
}

func TestMultiConnectionWithDynamicSplit(t *testing.T) {
	ts, srv := newTestServer(t, 8<<20)
	// 起点为 0 的分片很慢, 其它分片很快, 快的连接下完后应当切走慢分片的后半段.
	ts.delay = func(start int64) time.Duration {
		if start == 0 {
			return 20 * time.Millisecond
		}
		return 0
	}
	h := newHarness(t, srv.URL)
	job, _ := h.job(int64(len(ts.data)), true, nil, nil)
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	h.assertSame(ts.data)
	if job.Stats().Splits == 0 {
		t.Fatal("慢分片没有被动态分裂")
	}
}

func TestSingleConnectionWhenRangeIgnored(t *testing.T) {
	ts, srv := newTestServer(t, 3<<20)
	ts.ignoreRange = true
	h := newHarness(t, srv.URL)
	probe, err := h.client.Probe(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Resumable || probe.Size != int64(len(ts.data)) {
		t.Fatalf("探测结果不对: %+v", probe)
	}
	job, _ := h.job(probe.Size, probe.Resumable, nil, nil)
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	h.assertSame(ts.data)
}

func TestCancelAndResume(t *testing.T) {
	ts, srv := newTestServer(t, 8<<20)
	ts.delay = func(int64) time.Duration { return 3 * time.Millisecond }
	h := newHarness(t, srv.URL)
	size := int64(len(ts.data))

	ctx, cancel := context.WithCancel(context.Background())
	job, _ := h.job(size, true, nil, nil)
	go func() {
		for job.Downloaded() < size/3 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if err := job.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望被取消, 实际 %v", err)
	}
	snapshot := h.saved
	var reused int64
	for _, p := range snapshot {
		reused += p.Current - p.From
	}
	if reused <= 0 || reused >= size {
		t.Fatalf("快照进度不合理: %d / %d", reused, size)
	}

	before := len(ts.requestStarts())
	job2, _ := h.job(size, true, snapshot, nil)
	if err := job2.Run(context.Background()); err != nil {
		t.Fatalf("续传失败: %v", err)
	}
	h.assertSame(ts.data)
	// 续传时的每个请求起点都必须落在快照中尚未下载的区间里.
	for _, s := range ts.requestStarts()[before:] {
		ok := false
		for _, p := range snapshot {
			if s >= p.Current && s <= p.To {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("续传请求起点 %d 重复下载了已完成的数据", s)
		}
	}
}

func TestSizeChangedIsFatal(t *testing.T) {
	ts, srv := newTestServer(t, 1<<20)
	h := newHarness(t, srv.URL)
	job, _ := h.job(int64(len(ts.data))+100, true, nil, nil)
	if err := job.Run(context.Background()); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("期望 ErrFileChanged, 实际 %v", err)
	}
}

func TestRangeIgnoredMidwayIsFatal(t *testing.T) {
	ts, srv := newTestServer(t, 2<<20)
	ts.ignoreRange = true
	h := newHarness(t, srv.URL)
	// 按支持 Range 的结果下载, 但服务器对 Range 请求返回 200.
	job, _ := h.job(int64(len(ts.data)), true, nil, nil)
	if err := job.Run(context.Background()); !errors.Is(err, ErrRangeIgnored) {
		t.Fatalf("期望 ErrRangeIgnored, 实际 %v", err)
	}
}

func TestStalledConnectionReconnects(t *testing.T) {
	ts, srv := newTestServer(t, 2<<20)
	ts.hangOnce.Store(true)
	h := newHarness(t, srv.URL)
	job, _ := h.job(int64(len(ts.data)), true, nil, func(o *Options) {
		o.StallTimeout = 200 * time.Millisecond
		o.DynamicSplit = false
	})
	if err := job.Run(context.Background()); err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	h.assertSame(ts.data)
	if job.Stats().Retries == 0 {
		t.Fatal("卡住的连接没有触发重连")
	}
}
