// dn 是多连接分片下载命令行工具.
package main

import (
	"os"

	"github.com/azazo1/download-cli/internal/buildinfo"
	"github.com/azazo1/download-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute(buildinfo.Version()))
}
