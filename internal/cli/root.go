// Package cli 定义 dn 的命令行接口.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// 退出码.
const (
	ExitOK          = 0
	ExitFailed      = 1
	ExitUsage       = 2
	ExitInterrupted = 130
)

// exitError 携带退出码.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usageErr(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

// Execute 运行命令行, 返回进程退出码.
func Execute(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := newRootCmd(version)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.code != ExitInterrupted {
			fmt.Fprintln(os.Stderr, "错误:", ee.err)
		}
		return ee.code
	}
	// cobra 自身的参数解析错误.
	fmt.Fprintln(os.Stderr, "错误:", err)
	return ExitUsage
}

func newRootCmd(version string) *cobra.Command {
	f := &downloadFlags{}
	cmd := &cobra.Command{
		Use:   "dn <url> [url...]",
		Short: "多连接分片下载工具, 支持动态分片与断点续传",
		Long: `dn 把文件分成多个区间并用多个连接同时下载.
有连接空闲时, 会把剩余最多的分片从中间切开交给空闲连接 (动态分片).
下载被中断后, 重新运行同一条命令即可从断点继续.`,
		Version: version,
		Example: `  dn https://example.com/file.iso
  dn -d ~/Downloads -n 16 https://example.com/file.iso
  dn -o renamed.zip -H "Referer: https://example.com" https://example.com/a.zip`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return usageErr("至少需要 1 个链接, 例如: dn https://example.com/file.iso")
			}
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDownload(cmd, f, args)
		},
	}
	f.register(cmd)
	cmd.AddCommand(newServeCmd(), newConfigCmd(), newVersionCmd(version))
	// --version 只输出版本号本身, 便于脚本与发布流程精确断言.
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr("%v", err) })
	return cmd
}
