// Package config 负责配置文件定位, 读取, 校验与版本迁移.
//
// 配置目录解析优先级: DN_CONFIG_DIR 环境变量 > XDG_CONFIG_HOME > APPDATA (Windows) > ~/.config/dn.
package config

import (
	"os"
	"path/filepath"
	"runtime"
)

const (
	appDirName = "dn"
	// EnvConfigDir 用于覆盖配置目录.
	EnvConfigDir = "DN_CONFIG_DIR"
)

// Dir 返回配置目录的绝对路径.
func Dir() string {
	if custom := os.Getenv(EnvConfigDir); custom != "" {
		return ExpandHome(custom)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(ExpandHome(xdg), appDirName)
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, appDirName)
		}
	}
	return filepath.Join(homeDir(), ".config", appDirName)
}

// DefaultPath 返回默认配置文件路径.
func DefaultPath() string { return filepath.Join(Dir(), "config.toml") }

func homeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "."
}

// ExpandHome 展开路径开头的 ~.
func ExpandHome(path string) string {
	if path == "~" {
		return homeDir()
	}
	if len(path) > 2 && path[0] == '~' && (path[1] == '/' || path[1] == '\\') {
		return filepath.Join(homeDir(), path[2:])
	}
	return path
}
