package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/azazo1/download-cli/internal/config"
	"github.com/azazo1/download-cli/internal/httpx"
	"github.com/azazo1/download-cli/internal/logging"
	"github.com/azazo1/download-cli/internal/serve"
	"github.com/azazo1/download-cli/internal/task"
	"github.com/azazo1/download-cli/internal/tui"
)

// serveFlags 是 serve 命令的参数.
type serveFlags struct {
	dir            string
	port           int
	apiKey         string
	apiKeyEnforced bool
	noTUI          bool
	configPath     string
	logFile        string
	verbose        bool
	quiet          bool
}

func newServeCmd() *cobra.Command {
	f := &serveFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动本地集成服务, 兼容 AB Download Manager 的浏览器扩展",
		Long: `dn serve 在本机回环地址上启动一个 HTTP 服务, 协议与 AB Download Manager 的浏览器集成一致.

在浏览器里装好 AB Download Manager 扩展后, 把扩展选项里的端口改成这里的端口 (默认 15151),
扩展就会把捕获到的下载连同 Cookie 等请求头一起发给 dn.

终端里会显示任务列表, 可以暂停, 恢复, 重试和移除任务. 标准输出不是终端时只输出日志.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.dir, "dir", "d", "", "下载目录 (默认取配置 download_dir, 为空时为当前目录)")
	fl.IntVar(&f.port, "port", 0, "监听端口, 1024 到 65535 (默认取配置 serve_port, 初始为 15151)")
	fl.StringVar(&f.apiKey, "api-key", "", "浏览器集成的 api key, 留空表示不校验")
	fl.BoolVar(&f.apiKeyEnforced, "api-key-enforced", false, "浏览器集成端点也要求 api key")
	fl.BoolVar(&f.noTUI, "no-tui", false, "不启动终端界面, 只输出日志")
	fl.StringVar(&f.configPath, "config", "", "配置文件路径 (默认 ~/.config/dn/config.toml)")
	fl.StringVar(&f.logFile, "log-file", "", "日志同时写入该文件 (debug 级别)")
	fl.BoolVarP(&f.verbose, "verbose", "v", false, "输出调试日志")
	fl.BoolVarP(&f.quiet, "quiet", "q", false, "只输出警告和错误")
	return cmd
}

// resolve 合并命令行参数与配置文件.
func (f *serveFlags) resolve(cmd *cobra.Command) (config.Config, error) {
	path := f.configPath
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, _, err := config.Load(path)
	if err != nil {
		return config.Config{}, usageErr("%v", err)
	}
	changed := cmd.Flags().Changed
	if changed("dir") {
		cfg.DownloadDir = f.dir
	}
	if changed("port") {
		cfg.ServePort = f.port
	}
	if changed("api-key") {
		cfg.ServeAPIKey = f.apiKey
	}
	if changed("api-key-enforced") {
		cfg.ServeAPIKeyEnforced = f.apiKeyEnforced
	}
	switch {
	case f.verbose:
		cfg.LogLevel = "debug"
	case f.quiet:
		cfg.LogLevel = "warn"
	}
	if err := cfg.Validate(); err != nil {
		return config.Config{}, usageErr("%v", err)
	}
	return cfg, nil
}

func runServe(cmd *cobra.Command, f *serveFlags) error {
	cfg, err := f.resolve(cmd)
	if err != nil {
		return err
	}
	dir := cfg.ResolvedDownloadDir()

	// 只有标准输入输出都是终端时才启动界面, 否则重定向和后台运行会被界面破坏.
	useTUI := !f.noTUI && term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stdin.Fd()))
	var console io.Writer = os.Stderr
	var logs *tui.LogBuffer
	if useTUI {
		logs = tui.NewLogBuffer(800)
		console = logs
	}
	logOpts := logging.Options{Level: cfg.LogLevel, Console: console}
	if f.logFile != "" {
		lf, err := os.OpenFile(f.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return usageErr("打开日志文件失败: %v", err)
		}
		defer lf.Close()
		logOpts.File = lf
	}
	log := logging.New(logOpts)

	minPart, _ := config.ParseSize(cfg.MinPartSize)
	stall, _ := config.ParseDuration(cfg.StallTimeout)
	srv, err := serve.New(serve.Options{
		Port:          cfg.ServePort,
		Dir:           dir,
		APIKey:        cfg.ServeAPIKey,
		EnforceAPIKey: cfg.ServeAPIKeyEnforced,
		Task: task.Options{
			Dir: dir, Connections: cfg.Connections, MinPartSize: minPart,
			DynamicSplit: cfg.DynamicSplit, Retries: cfg.Retries, StallTimeout: stall,
		},
		Client: httpx.Options{UserAgent: cfg.UserAgent, Proxy: cfg.Proxy, Headers: cfg.Headers},
		Logger: log,
	})
	if err != nil {
		return usageErr("%v", err)
	}
	// 先占住端口再进界面, 端口冲突时直接给出错误而不是让界面一闪而过.
	if err := srv.Listen(); err != nil {
		return usageErr("%v", err)
	}

	ctx := cmd.Context()
	if !useTUI {
		return srv.Run(ctx)
	}

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	program := tea.NewProgram(tui.New(srv, logs, dir), tea.WithAltScreen())
	runErr := make(chan error, 1)
	go func() {
		err := srv.Run(serveCtx)
		runErr <- err
		program.Quit()
	}()
	if _, err := program.Run(); err != nil {
		cancel()
		return err
	}
	// 界面退出表示用户要停止服务.
	cancel()
	if err := <-runErr; err != nil {
		return usageErr("%v", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已退出, 进行中的下载进度已保存")
	return nil
}
