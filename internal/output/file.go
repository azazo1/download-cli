package output

import (
	"fmt"
	"os"
)

// OpenPart 打开数据文件. size >= 0 时用 Truncate 预分配为稀疏文件;
// fresh 为 true 时清空已有内容.
func OpenPart(path string, size int64, fresh bool) (*os.File, error) {
	flag := os.O_RDWR | os.O_CREATE
	if fresh {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开文件 %s 失败: %w", path, err)
	}
	if size >= 0 {
		if err := f.Truncate(size); err != nil {
			f.Close()
			return nil, fmt.Errorf("预分配文件 %s 失败: %w", path, err)
		}
	}
	return f, nil
}

// PartSize 返回数据文件大小, 文件不存在时返回 -1.
func PartSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

// Finalize 关闭数据文件并把 .part 改名为最终文件名.
// 长度未知的下载会按实际写入量截断尾部.
func Finalize(f *os.File, t Target, actualSize int64) error {
	if actualSize >= 0 {
		if err := f.Truncate(actualSize); err != nil {
			f.Close()
			return fmt.Errorf("截断文件失败: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭文件失败: %w", err)
	}
	if err := os.Rename(t.PartPath(), t.Final); err != nil {
		return fmt.Errorf("重命名 %s 失败: %w", t.PartPath(), err)
	}
	return nil
}
