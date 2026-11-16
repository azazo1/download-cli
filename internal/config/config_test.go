package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("download_dir = \"~/dl\"\nconnections = 16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, existed, err := Load(path)
	if err != nil || !existed {
		t.Fatalf("v0 配置应当能迁移: %v", err)
	}
	if cfg.Version != CurrentVersion || cfg.Connections != 16 || cfg.MinPartSize == "" || !cfg.DynamicSplit {
		t.Fatalf("迁移结果不对: %+v", cfg)
	}
	if err := os.WriteFile(path, []byte("version = 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil {
		t.Fatal("版本过高的配置应当报错")
	}
	if _, existed, err := Load(filepath.Join(dir, "none.toml")); err != nil || existed {
		t.Fatal("配置不存在时应返回默认值")
	}
}

// TestDefaultTemplateMatchesDefaults 保证 config init 写出的模板不会与内置默认值漂移.
func TestDefaultTemplateMatchesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := WriteDefault(path, false); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatalf("模板无法解析: %v", err)
	}
	if got.Version != Default().Version || got.Connections != Default().Connections ||
		got.MinPartSize != Default().MinPartSize || got.DynamicSplit != Default().DynamicSplit ||
		got.Retries != Default().Retries || got.StallTimeout != Default().StallTimeout ||
		got.LogLevel != Default().LogLevel || got.DownloadDir != "" || got.UserAgent != "" || got.Proxy != "" {
		t.Fatalf("模板内容与默认值不一致: %+v", got)
	}
	if err := WriteDefault(path, false); err == nil {
		t.Fatal("重复写入应当报错")
	}
	if err := WriteDefault(path, true); err != nil {
		t.Fatalf("--force 应当允许覆盖: %v", err)
	}
}

// TestExampleFileMatchesTemplate 保证仓库里的示例配置与 config init 的模板一致.
func TestExampleFileMatchesTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.toml.example"))
	if err != nil {
		t.Fatalf("读取示例配置失败: %v", err)
	}
	if string(data) != DefaultTemplate() {
		t.Fatal("config.toml.example 与 config.DefaultTemplate() 不一致, 请同步两者")
	}
}
