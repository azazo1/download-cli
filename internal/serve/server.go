package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azazo1/download-cli/internal/config"
	"github.com/azazo1/download-cli/internal/httpx"
	"github.com/azazo1/download-cli/internal/logging"
	"github.com/azazo1/download-cli/internal/task"
)

// maxBodyBytes 是请求体大小上限, 正常情况下扩展只会发少量 JSON.
const maxBodyBytes = 4 << 20

// Options 是本地集成服务的参数.
type Options struct {
	Port int
	// Dir 是下载目录, 为空表示当前目录.
	Dir string
	// APIKey 为空表示不校验 api key.
	APIKey string
	// EnforceAPIKey 为 true 时, 浏览器集成端点也要求 api key.
	EnforceAPIKey bool

	// Task 是每个下载任务的参数模板, Dir 由 Options.Dir 覆盖.
	Task   task.Options
	Client httpx.Options
	Logger *slog.Logger
}

// Server 是兼容 AB Download Manager 浏览器扩展的本地服务.
type Server struct {
	opts   Options
	log    *slog.Logger
	base   *httpx.Client
	listen []net.Listener

	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	tasks  []*taskState
	nextID int
	wg     sync.WaitGroup
}

// New 构造服务. 此时还没有开始监听.
func New(opts Options) (*Server, error) {
	if opts.Port < config.MinServePort || opts.Port > config.MaxServePort {
		return nil, fmt.Errorf("端口必须在 %d 到 %d 之间, 当前为 %d", config.MinServePort, config.MaxServePort, opts.Port)
	}
	if strings.TrimSpace(opts.Dir) == "" {
		opts.Dir = "."
	}
	if opts.Logger == nil {
		opts.Logger = logging.Discard()
	}
	client, err := httpx.NewClient(opts.Client)
	if err != nil {
		return nil, err
	}
	opts.Task.Dir = opts.Dir
	return &Server{opts: opts, log: opts.Logger, base: client}, nil
}

// Address 返回扩展应该填写的服务地址.
func (s *Server) Address() string {
	return fmt.Sprintf("http://localhost:%d", s.opts.Port)
}

// APIKey 返回当前使用的 api key, 供启动提示使用.
func (s *Server) APIKey() string { return s.opts.APIKey }

// Listen 先占住端口, 便于调用方在启动界面前就发现端口冲突. 重复调用无副作用.
func (s *Server) Listen() error {
	if s.listen != nil {
		return nil
	}
	listeners, err := listenLoopback(s.opts.Port)
	if err != nil {
		return err
	}
	s.listen = listeners
	return nil
}

// Run 开始监听并阻塞到 ctx 结束. 退出时会取消进行中的任务, 让它们保存续传状态.
func (s *Server) Run(ctx context.Context) error {
	s.ctx, s.cancel = context.WithCancel(ctx)
	defer s.cancel()

	if err := s.Listen(); err != nil {
		return err
	}
	listeners := s.listen
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()

	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          log.New(newErrorLogger(s.log), "", 0),
	}
	serveErr := make(chan error, len(listeners))
	for _, l := range listeners {
		go func(l net.Listener) {
			if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
			}
		}(l)
	}

	s.log.Info("浏览器集成已启动", "地址", s.Address(), "下载目录", s.opts.Dir)
	if s.opts.EnforceAPIKey {
		s.log.Info("浏览器集成要求 api key", "请求头", "X-Api-Key")
	}

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		s.log.Error("HTTP 服务异常退出", "错误", err)
	}
	// 无论因为什么原因停止, 都要取消进行中的任务, 让它们保存续传状态.
	s.cancel()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Debug("HTTP 服务关闭时出错", "错误", err)
	}
	// 等待进行中的任务把续传状态写完再返回.
	s.wg.Wait()
	s.log.Info("浏览器集成已停止")
	return nil
}

// listenLoopback 同时监听 IPv4 与 IPv6 的回环地址.
// 浏览器扩展连接的是 localhost, 在双栈系统上可能先解析到 ::1.
func listenLoopback(port int) ([]net.Listener, error) {
	var listeners []net.Listener
	v4, err4 := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err4 == nil {
		listeners = append(listeners, v4)
	}
	v6, err6 := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port))
	if err6 == nil {
		listeners = append(listeners, v6)
	}
	if len(listeners) == 0 {
		return nil, fmt.Errorf("监听 %d 端口失败 (是否已有其他程序占用?): %w", port, err4)
	}
	return listeners, nil
}

// handler 组装路由, 并在最外层做来源检查.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/add", s.handleAdd)
	mux.HandleFunc("/queues", s.handleQueues)
	mux.HandleFunc("/start-headless-download", s.handleHeadless)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestAllowed(r) {
			s.log.Warn("拒绝非扩展来源的请求", "路径", r.URL.Path, "来源", r.Header.Get("Origin"))
			http.Error(w, "只接受浏览器扩展或本机命令行发起的请求", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// handlePing 供扩展探测本地程序是否在线.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, false) {
		unauthorized(w)
		return
	}
	s.log.Debug("收到 ping", "方法", r.Method)
	fmt.Fprint(w, "pong")
}

// handleAdd 接收扩展提交的下载. 与 ABDM 一样, 只要受理就返回 OK, 下载在后台进行.
func (s *Server) handleAdd(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, false) {
		unauthorized(w)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := parseAdd(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(items) == 0 {
		http.Error(w, "没有可下载的链接", http.StatusBadRequest)
		return
	}
	for _, item := range items {
		if err := validateItem(item); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	for _, item := range items {
		t := s.submit(item, "")
		s.log.Info("收到下载请求", "任务", t.id, "链接", item.Link,
			"生效请求头", headerNames(cleanHeaders(item.Headers)), "建议文件名", item.SuggestedName)
	}
	fmt.Fprint(w, "OK")
}

// handleQueues 返回空队列, 让只认 ABDM 队列概念的客户端也能正常工作.
func (s *Server) handleQueues(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, false) {
		unauthorized(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, "[]")
}

// handleHeadless 供命令行或脚本提交下载, 总是要求 api key.
func (s *Server) handleHeadless(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r, true) {
		unauthorized(w)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := readBody(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var dt downloadTask
	if err := json.Unmarshal(body, &dt); err != nil {
		http.Error(w, "请求体不是有效的下载任务: "+err.Error(), http.StatusBadRequest)
		return
	}
	item := dt.DownloadSource
	if name := strings.TrimSpace(dt.Name); name != "" {
		item.SuggestedName = name
	}
	if err := validateItem(item); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t := s.submit(item, strings.TrimSpace(dt.Folder))
	s.log.Info("收到无界面下载请求", "任务", t.id, "链接", item.Link, "目录", dt.Folder,
		"生效请求头", headerNames(cleanHeaders(item.Headers)))
	fmt.Fprint(w, "OK")
}

// authorized 判断请求是否通过 api key 校验.
// required 为 true 的端点 (无界面下载) 在未配置 key 时一律拒绝.
func (s *Server) authorized(r *http.Request, required bool) bool {
	key := strings.TrimSpace(s.opts.APIKey)
	if key == "" {
		return !required
	}
	if r.Header.Get("X-Api-Key") == key {
		return true
	}
	if required {
		return false
	}
	return !s.opts.EnforceAPIKey
}

func unauthorized(w http.ResponseWriter) {
	http.Error(w, "api key 无效或缺失 (请求头 X-Api-Key)", http.StatusUnauthorized)
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	http.Error(w, "不支持的请求方法", http.StatusMethodNotAllowed)
}

// readBody 读取请求体, 并限制大小.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	return body, nil
}

// headerNames 返回请求头的名字 (已排序), 供日志记录, 不输出值.
func headerNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// errorLogger 把 net/http 自己的错误日志转进 slog, 避免直接写 stderr 破坏 TUI.
func newErrorLogger(log *slog.Logger) *logWriter {
	return &logWriter{log: log}
}

type logWriter struct {
	log *slog.Logger
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.log.Debug("HTTP 服务", "信息", strings.TrimSpace(string(p)))
	return len(p), nil
}
