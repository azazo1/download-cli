package serve

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azazo1/download-cli/internal/logging"
	"github.com/azazo1/download-cli/internal/task"
)

// slowWriter 给每次响应写入加上延迟, 用来制造可以暂停的下载.
type slowWriter struct {
	http.ResponseWriter
	delay time.Duration
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.ResponseWriter.Write(p)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startServer 启动服务并返回它, 测试结束时自动关闭.
func startServer(t *testing.T, port int, dir string, opts task.Options) *Server {
	t.Helper()
	opts.Dir = dir
	s, err := New(Options{Port: port, Dir: dir, Logger: logging.Discard(), Task: opts})
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("服务退出出错: %v", err)
		}
	})
	waitPing(t, port)
	return s
}

func waitPing(t *testing.T, port int) {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/ping", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Post(url, "text/plain", strings.NewReader("null"))
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) == "pong" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("服务没有在预期时间内就绪")
}

func postAdd(t *testing.T, port int, body string) {
	t.Helper()
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/add", port), "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("提交下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("提交下载返回 %d: %s", resp.StatusCode, msg)
	}
}

func waitStatus(t *testing.T, s *Server, want Status, timeout time.Duration) Snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		snaps := s.Snapshots()
		if len(snaps) > 0 && snaps[0].Status == want {
			return snaps[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待任务进入 %s 超时, 当前: %+v", want, s.Snapshots())
	return Snapshot{}
}

func TestAddDownloadCarriesHeaders(t *testing.T) {
	content := bytes.Repeat([]byte("dn"), 64*1024)
	var mu sync.Mutex
	var cookies []string
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cookies = append(cookies, r.Header.Get("Cookie"))
		mu.Unlock()
		http.ServeContent(w, r, "blob.bin", time.Now(), bytes.NewReader(content))
	}))
	defer fileSrv.Close()

	port := freePort(t)
	dir := t.TempDir()
	s := startServer(t, port, dir, task.Options{Connections: 4, MinPartSize: 64 << 10})

	body := fmt.Sprintf(`{"items":[{"link":%q,"headers":{"Cookie":"sid=abc","Host":"ignored.example","User-Agent":"TestUA"}}],"options":{"silentAdd":true}}`,
		fileSrv.URL+"/blob.bin")
	postAdd(t, port, body)
	snap := waitStatus(t, s, StatusDone, 10*time.Second)

	data, err := os.ReadFile(snap.Path)
	if err != nil {
		t.Fatalf("读取下载结果失败: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("下载内容不一致: 期望 %d 字节, 实际 %d 字节", len(content), len(data))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(cookies) == 0 {
		t.Fatal("服务器没有收到任何请求")
	}
	for _, c := range cookies {
		if c != "sid=abc" {
			t.Fatalf("Cookie 没有带到下载请求里, 实际为 %q", c)
		}
	}
}

func TestPauseAndResume(t *testing.T) {
	content := bytes.Repeat([]byte("z"), 1<<20)
	fileSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(&slowWriter{ResponseWriter: w, delay: 50 * time.Millisecond}, r,
			"slow.bin", time.Now(), bytes.NewReader(content))
	}))
	defer fileSrv.Close()

	port := freePort(t)
	dir := t.TempDir()
	s := startServer(t, port, dir, task.Options{Connections: 1, MinPartSize: 64 << 10})

	postAdd(t, port, fmt.Sprintf(`{"items":[{"link":%q}]}`, fileSrv.URL+"/slow.bin"))

	// 等到确实下载了一部分再暂停, 避免任务已经结束.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snaps := s.Snapshots()
		if len(snaps) > 0 && snaps[0].Downloaded > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := s.Pause(snapsID(s.Snapshots())); err != nil {
		t.Fatalf("暂停失败: %v", err)
	}
	paused := waitStatus(t, s, StatusPaused, 5*time.Second)
	if paused.Downloaded >= int64(len(content)) {
		t.Fatalf("暂停时已经下载完, 无法验证续传: %d", paused.Downloaded)
	}

	if err := s.Resume(paused.ID); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	done := waitStatus(t, s, StatusDone, 20*time.Second)
	data, err := os.ReadFile(done.Path)
	if err != nil {
		t.Fatalf("读取下载结果失败: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Fatalf("续传后内容不一致: 期望 %d 字节, 实际 %d 字节", len(content), len(data))
	}
}

func snapsID(snaps []Snapshot) int {
	if len(snaps) == 0 {
		return 0
	}
	return snaps[0].ID
}
