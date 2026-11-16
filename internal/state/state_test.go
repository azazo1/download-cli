package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrate(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.part.json")
	if err := os.WriteFile(old, []byte(`{"url":"u","size":10,"resumable":true,"parts":[{"from":0,"to":9,"current":4}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(old)
	if err != nil {
		t.Fatalf("v0 状态应当能迁移: %v", err)
	}
	if s.Version != CurrentVersion || s.Downloaded() != 4 {
		t.Fatalf("迁移结果不对: %+v", s)
	}
	future := filepath.Join(dir, "future.part.json")
	if err := os.WriteFile(future, []byte(`{"version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(future); err == nil {
		t.Fatal("版本过高的状态文件应当报错")
	}
}
