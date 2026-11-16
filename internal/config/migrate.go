package config

import "fmt"

// migration 描述一次配置版本跃迁.
type migration struct {
	// To 是迁移完成后的版本号.
	To int
	// Describe 是一句话说明.
	Describe string
	// Apply 执行迁移.
	Apply func(*Config) error
}

// migrations 按目标版本号升序排列.
var migrations = []migration{
	{To: 1, Describe: "初始版本, 补齐缺失的基础字段", Apply: migrateToV1},
}

// migrate 把配置提升到 CurrentVersion, 返回是否发生了变更.
func migrate(cfg *Config) (bool, error) {
	if cfg.Version > CurrentVersion {
		return false, fmt.Errorf("配置文件版本 %d 高于程序支持的版本 %d, 请升级 dn", cfg.Version, CurrentVersion)
	}
	changed := false
	for _, m := range migrations {
		if cfg.Version < m.To {
			if err := m.Apply(cfg); err != nil {
				return false, fmt.Errorf("迁移配置到 v%d 失败: %w", m.To, err)
			}
			cfg.Version = m.To
			changed = true
		}
	}
	return changed, nil
}

// migrateToV1 为零值字段补默认值.
func migrateToV1(cfg *Config) error {
	d := Default()
	if cfg.Connections == 0 {
		cfg.Connections = d.Connections
	}
	if cfg.MinPartSize == "" {
		cfg.MinPartSize = d.MinPartSize
	}
	if cfg.StallTimeout == "" {
		cfg.StallTimeout = d.StallTimeout
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = d.LogLevel
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	return nil
}
