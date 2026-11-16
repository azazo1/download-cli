// Package state 读写续传状态文件 (<name>.part.json).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/azazo1/download-cli/internal/engine"
)

// CurrentVersion 是本程序写出和理解的状态文件版本号.
const CurrentVersion = 1

// State 是续传所需的全部信息.
type State struct {
	Version      int                `json:"version"`
	URL          string             `json:"url"`
	FinalURL     string             `json:"final_url"`
	Size         int64              `json:"size"`
	ETag         string             `json:"etag,omitempty"`
	LastModified string             `json:"last_modified,omitempty"`
	Resumable    bool               `json:"resumable"`
	Parts        []engine.PartState `json:"parts"`
}

// ErrNotFound 表示状态文件不存在.
var ErrNotFound = errors.New("续传状态不存在")

// Load 读取并迁移状态文件.
func Load(path string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("状态文件 %s 已损坏: %w", path, err)
	}
	if err := migrate(&s); err != nil {
		return State{}, err
	}
	return s, nil
}

// Save 原子写入状态文件: 先写同目录临时文件, 再 rename 覆盖.
func Save(path string, s State) error {
	s.Version = CurrentVersion
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// Remove 删除状态文件, 不存在时不报错.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Downloaded 返回状态中已下载的字节数.
func (s State) Downloaded() int64 {
	var n int64
	for _, p := range s.Parts {
		n += p.Current - p.From
	}
	return n
}
