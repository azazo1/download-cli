package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/pelletier/go-toml/v2"
)

// CurrentVersion 是本程序写出和理解的配置版本号.
const CurrentVersion = 1

// 连接数允许的范围.
const (
	MinConnections = 1
	MaxConnections = 64
)

// Config 是配置文件的内存表示. 大小与时长以文本保存, 便于手写.
type Config struct {
	Version      int               `toml:"version"`
	DownloadDir  string            `toml:"download_dir"`
	Connections  int               `toml:"connections"`
	MinPartSize  string            `toml:"min_part_size"`
	DynamicSplit bool              `toml:"dynamic_split"`
	Retries      int               `toml:"retries"`
	StallTimeout string            `toml:"stall_timeout"`
	UserAgent    string            `toml:"user_agent"`
	Proxy        string            `toml:"proxy"`
	LogLevel     string            `toml:"log_level"`
	Headers      map[string]string `toml:"headers"`
}

// Default 返回内置默认配置.
func Default() Config {
	return Config{
		Version:      CurrentVersion,
		DownloadDir:  "",
		Connections:  8,
		MinPartSize:  "512KiB",
		DynamicSplit: true,
		Retries:      5,
		StallTimeout: "30s",
		LogLevel:     "info",
		Headers:      map[string]string{},
	}
}

// Load 读取配置文件. 文件不存在时返回默认配置, existed 为 false.
func Load(path string) (cfg Config, existed bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	cfg = Default()
	// 未写 version 的文件视为 0 版本, 交给迁移补齐.
	cfg.Version = 0
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, true, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	if _, err := migrate(&cfg); err != nil {
		return Config{}, true, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, true, fmt.Errorf("配置文件 %s 无效: %w", path, err)
	}
	return cfg, true, nil
}

// Validate 校验字段取值.
func (c Config) Validate() error {
	if c.Connections < MinConnections || c.Connections > MaxConnections {
		return fmt.Errorf("connections 必须在 %d 到 %d 之间, 当前为 %d", MinConnections, MaxConnections, c.Connections)
	}
	if _, err := ParseSize(c.MinPartSize); err != nil {
		return fmt.Errorf("min_part_size: %w", err)
	}
	if c.Retries < 0 {
		return fmt.Errorf("retries 不能为负数")
	}
	if _, err := ParseDuration(c.StallTimeout); err != nil {
		return fmt.Errorf("stall_timeout: %w", err)
	}
	return nil
}

// ResolvedDownloadDir 返回展开后的下载目录, 为空时表示当前目录.
func (c Config) ResolvedDownloadDir() string {
	if strings.TrimSpace(c.DownloadDir) == "" {
		return "."
	}
	return ExpandHome(c.DownloadDir)
}

// ParseSize 解析 512KiB, 2M 之类的大小文本, 结果至少为 1KiB.
func ParseSize(text string) (int64, error) {
	n, err := humanize.ParseBytes(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("无法解析大小 %q: %w", text, err)
	}
	if n < 1024 {
		return 0, fmt.Errorf("大小 %q 不能小于 1KiB", text)
	}
	if n > 2<<30 {
		return 0, fmt.Errorf("大小 %q 不能超过 2GiB", text)
	}
	return int64(n), nil
}

// ParseDuration 解析时长文本, 必须为正数.
func ParseDuration(text string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return 0, fmt.Errorf("无法解析时长 %q: %w", text, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("时长 %q 必须为正数", text)
	}
	return d, nil
}

// Encode 把配置序列化为 TOML 文本.
func Encode(cfg Config) ([]byte, error) {
	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	if err := enc.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DefaultTemplate 返回带注释的默认配置文本, 供 config init 写出.
func DefaultTemplate() string {
	return `# dn 配置文件. 所有字段都可以省略, 省略时使用内置默认值.
# 命令行参数的优先级高于此文件.

# 配置版本号, 由程序维护, 不要手动修改.
version = 1

# 下载目录. 为空表示当前目录; 支持 ~ 展开.
download_dir = ""

# 最大并发连接数, 范围 1 到 64.
connections = 8

# 最小分片大小. 分片再小就不值得再切了.
min_part_size = "512KiB"

# 有连接空闲时是否把剩余最多的分片切开, 交给空闲连接.
dynamic_split = true

# 单个分片在没有进展时允许的连续失败次数.
retries = 5

# 单个连接多久收不到数据就判定卡死并重连.
stall_timeout = "30s"

# 空值表示使用内置 User-Agent.
user_agent = ""

# 空值表示读取 HTTP_PROXY / HTTPS_PROXY 环境变量.
proxy = ""

# 日志级别: debug / info / warn / error.
log_level = "info"

# 附加请求头, 每个请求都会带上.
[headers]
# Referer = "https://example.com"
`
}

// WriteDefault 把默认配置写到 path. 文件已存在且 force 为 false 时返回错误.
func WriteDefault(path string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("配置文件 %s 已存在, 使用 --force 覆盖", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(DefaultTemplate()), 0o644)
}
