// dn 是多连接分片下载命令行工具.
package main

import (
	"os"

	"github.com/azazo1/download-cli/internal/cli"
)

// version 在构建时通过 -ldflags "-X main.version=..." 注入.
var version = "dev-build"

func main() {
	os.Exit(cli.Execute(version))
}
