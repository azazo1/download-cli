// Package output 负责目标文件命名, 预分配与完成后的落盘.
package output

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// PartSuffix 是下载中数据文件的后缀.
	PartSuffix = ".part"
	// StateSuffix 是续传状态文件的后缀.
	StateSuffix  = ".part.json"
	maxNameBytes = 255
	// 预留给 ".part.json" 与 " (999)" 的空间.
	reservedBytes = 20
)

// Target 描述一次下载涉及的三个路径.
type Target struct {
	// Final 是下载完成后的文件路径.
	Final string
}

// PartPath 返回数据文件路径.
func (t Target) PartPath() string { return t.Final + PartSuffix }

// StatePath 返回续传状态文件路径.
func (t Target) StatePath() string { return t.Final + StateSuffix }

// Name 返回文件名.
func (t Target) Name() string { return filepath.Base(t.Final) }

var reserved = strings.NewReplacer(
	"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
	"\"", "_", "<", "_", ">", "_", "|", "_",
)

// SanitizeName 清洗服务器或 URL 给出的文件名, 结果为空时返回 "download".
func SanitizeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = reserved.Replace(strings.TrimSpace(name))
	// Windows 不允许以点或空格结尾; 也避免 "." 与 ".." 这类特殊名字.
	name = strings.TrimRight(name, ". ")
	name = strings.TrimLeft(name, " ")
	if name == "" || strings.Trim(name, ".") == "" {
		return "download"
	}
	return truncateName(name, maxNameBytes-reservedBytes)
}

// truncateName 在保留扩展名的前提下把文件名截到 limit 字节以内, 不截断 UTF-8 字符.
func truncateName(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	base := name[:len(name)-len(ext)]
	cut := limit - len(ext)
	for cut > 0 && !utf8.RuneStart(base[cut]) {
		cut--
	}
	return base[:cut] + ext
}

// Resolve 根据目录与文件名生成目标. 不覆盖时, 已存在的文件 (含 .part) 会被加序号避开.
func Resolve(dir, name string, overwrite bool) (Target, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Target{}, fmt.Errorf("创建目录 %s 失败: %w", dir, err)
	}
	t := Target{Final: filepath.Join(dir, name)}
	if overwrite {
		return t, nil
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; occupied(t); i++ {
		if i > 999 {
			return Target{}, fmt.Errorf("目录 %s 中同名文件过多", dir)
		}
		t = Target{Final: filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))}
	}
	return t, nil
}

func occupied(t Target) bool {
	for _, p := range []string{t.Final, t.PartPath(), t.StatePath()} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}
