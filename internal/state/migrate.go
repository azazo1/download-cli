package state

import "fmt"

// migration 描述一次状态文件版本跃迁.
type migration struct {
	To       int
	Describe string
	Apply    func(*State) error
}

// migrations 按目标版本号升序排列.
var migrations = []migration{
	{To: 1, Describe: "初始版本", Apply: func(*State) error { return nil }},
}

// migrate 把状态提升到 CurrentVersion. 版本过高时返回错误, 调用方应从头下载.
func migrate(s *State) error {
	if s.Version > CurrentVersion {
		return fmt.Errorf("状态文件版本 %d 高于程序支持的版本 %d", s.Version, CurrentVersion)
	}
	for _, m := range migrations {
		if s.Version < m.To {
			if err := m.Apply(s); err != nil {
				return fmt.Errorf("迁移状态文件到 v%d 失败: %w", m.To, err)
			}
			s.Version = m.To
		}
	}
	return nil
}
