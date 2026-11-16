package cli

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/azazo1/download-cli/internal/config"
	"github.com/azazo1/download-cli/internal/httpx"
	"github.com/azazo1/download-cli/internal/logging"
	"github.com/azazo1/download-cli/internal/task"
)

// downloadFlags 是下载命令的参数. 未显式指定的参数取配置文件的值.
type downloadFlags struct {
	dir          string
	output       string
	connections  int
	minPartSize  string
	noDynamic    bool
	retries      int
	stallTimeout string
	headers      []string
	userAgent    string
	proxy        string
	overwrite    bool
	restart      bool
	configPath   string
	verbose      bool
	quiet        bool
	logFile      string
}

func (f *downloadFlags) register(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVarP(&f.dir, "dir", "d", "", "目标目录, 不存在时自动创建 (默认取配置 download_dir, 为空时为当前目录)")
	fl.StringVarP(&f.output, "output", "o", "", "指定文件名, 只能和单个 URL 一起使用")
	fl.IntVarP(&f.connections, "connections", "n", 0, "最大并发连接数, 1 到 64 (默认 8)")
	fl.StringVar(&f.minPartSize, "min-part-size", "", "最小分片大小, 例如 512KiB, 2M (默认 512KiB)")
	fl.BoolVar(&f.noDynamic, "no-dynamic", false, "关闭动态分片")
	fl.IntVar(&f.retries, "retries", -1, "单个分片没有进展时允许的连续失败次数 (默认 5)")
	fl.StringVar(&f.stallTimeout, "stall-timeout", "", "连接多久没有收到数据就重连, 例如 30s")
	fl.StringArrayVarP(&f.headers, "header", "H", nil, `附加请求头, 格式 "Name: value", 可重复`)
	fl.StringVar(&f.userAgent, "user-agent", "", "User-Agent")
	fl.StringVar(&f.proxy, "proxy", "", "代理地址, 例如 http://127.0.0.1:7890 (默认读取 HTTP(S)_PROXY)")
	fl.BoolVar(&f.overwrite, "overwrite", false, "目标文件已存在时覆盖, 默认改名为 name (1).ext")
	fl.BoolVar(&f.restart, "restart", false, "忽略已有的续传状态, 从头下载")
	fl.StringVar(&f.configPath, "config", "", "配置文件路径 (默认 ~/.config/dn/config.toml)")
	fl.BoolVarP(&f.verbose, "verbose", "v", false, "输出调试日志")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "只输出警告和错误")
	fl.StringVar(&f.logFile, "log-file", "", "日志同时写入该文件 (debug 级别)")
}

// settings 是合并参数与配置后的结果.
type settings struct {
	task     task.Options
	client   httpx.Options
	logLevel string
}

func (f *downloadFlags) resolve(cmd *cobra.Command, urls []string) (settings, error) {
	path := f.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		return settings{}, usageErr("%v", err)
	}
	changed := cmd.Flags().Changed
	if changed("dir") {
		cfg.DownloadDir = f.dir
	}
	if changed("connections") {
		cfg.Connections = f.connections
	}
	if changed("min-part-size") {
		cfg.MinPartSize = f.minPartSize
	}
	if f.noDynamic {
		cfg.DynamicSplit = false
	}
	if changed("retries") {
		cfg.Retries = f.retries
	}
	if changed("stall-timeout") {
		cfg.StallTimeout = f.stallTimeout
	}
	if changed("user-agent") {
		cfg.UserAgent = f.userAgent
	}
	if changed("proxy") {
		cfg.Proxy = f.proxy
	}
	switch {
	case f.verbose:
		cfg.LogLevel = "debug"
	case f.quiet:
		cfg.LogLevel = "warn"
	}
	for _, h := range f.headers {
		name, value, ok := strings.Cut(h, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return settings{}, usageErr("请求头 %q 格式应为 \"Name: value\"", h)
		}
		cfg.Headers[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	if err := cfg.Validate(); err != nil {
		return settings{}, usageErr("%v", err)
	}
	if f.output != "" && len(urls) > 1 {
		return settings{}, usageErr("-o 只能和单个 URL 一起使用")
	}
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return settings{}, usageErr("不是有效的 http(s) 链接: %s", raw)
		}
	}
	minPart, _ := config.ParseSize(cfg.MinPartSize)
	stall, _ := config.ParseDuration(cfg.StallTimeout)
	return settings{
		task: task.Options{
			Dir: cfg.ResolvedDownloadDir(), Output: f.output, Connections: cfg.Connections,
			MinPartSize: minPart, DynamicSplit: cfg.DynamicSplit, Retries: cfg.Retries,
			StallTimeout: stall, Overwrite: f.overwrite, Restart: f.restart,
		},
		client:   httpx.Options{UserAgent: cfg.UserAgent, Proxy: cfg.Proxy, Headers: cfg.Headers},
		logLevel: cfg.LogLevel,
	}, nil
}

func runDownload(cmd *cobra.Command, f *downloadFlags, urls []string) error {
	s, err := f.resolve(cmd, urls)
	if err != nil {
		return err
	}
	logSwitch := logging.NewSwitchWriter(os.Stderr)
	logOpts := logging.Options{Level: s.logLevel, Console: logSwitch}
	if f.logFile != "" {
		lf, err := os.OpenFile(f.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return usageErr("打开日志文件失败: %v", err)
		}
		defer lf.Close()
		logOpts.File = lf
	}
	log := logging.New(logOpts)
	client, err := httpx.NewClient(s.client)
	if err != nil {
		return usageErr("%v", err)
	}
	log.Debug("请求配置", "附加请求头", client.HeaderNames(), "使用代理", s.client.Proxy != "")

	isTTY := term.IsTerminal(int(os.Stderr.Fd()))
	s.task.ShowBar = isTTY && !f.quiet
	s.task.ProgressOut = os.Stderr
	s.task.LogSwitch = logSwitch

	ctx := cmd.Context()
	var failed []string
	for _, raw := range urls {
		opts := s.task
		opts.URL = raw
		if _, err := task.Run(ctx, client, log, opts); err != nil {
			if errors.Is(err, task.ErrInterrupted) {
				return &exitError{code: ExitInterrupted, err: err}
			}
			log.Error("下载失败", "链接", raw, "错误", err)
			failed = append(failed, raw)
		}
	}
	if len(failed) > 0 {
		if len(urls) > 1 {
			log.Error("部分下载失败", "失败", len(failed), "总数", len(urls))
		}
		return &exitError{code: ExitFailed, err: fmt.Errorf("%d 个下载失败", len(failed))}
	}
	return nil
}
