package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/azazo1/download-cli/internal/serve"
)

// fakeController 是只记录调用的测试替身.
type fakeController struct {
	snaps   []serve.Snapshot
	paused  int
	resumed int
	removed int
}

func (f *fakeController) Address() string             { return "http://localhost:15151" }
func (f *fakeController) Snapshots() []serve.Snapshot { return f.snaps }
func (f *fakeController) Pause(int) error             { f.paused++; return nil }
func (f *fakeController) Resume(int) error            { f.resumed++; return nil }
func (f *fakeController) Remove(int) error            { f.removed++; return nil }

func isQuit(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestQuitNeedsConfirmationWhileRunning(t *testing.T) {
	ctrl := &fakeController{snaps: []serve.Snapshot{{ID: 1, Name: "a.bin", Status: serve.StatusDownloading}}}
	m := New(ctrl, NewLogBuffer(10), ".")

	next, cmd := m.Update(key("q"))
	m = next.(*Model)
	if isQuit(t, cmd) {
		t.Fatal("有任务下载时第一次 q 不应退出")
	}
	if !m.quitArm {
		t.Fatal("应该进入退出确认状态")
	}

	_, cmd = m.Update(key("q"))
	if !isQuit(t, cmd) {
		t.Fatal("确认后再次按 q 应退出")
	}
}

func TestQuitWithoutRunning(t *testing.T) {
	ctrl := &fakeController{snaps: []serve.Snapshot{{ID: 1, Status: serve.StatusDone}}}
	m := New(ctrl, NewLogBuffer(10), ".")
	if _, cmd := m.Update(key("q")); !isQuit(t, cmd) {
		t.Fatal("没有下载任务时 q 应立即退出")
	}
}

func TestTogglePausesAndResumes(t *testing.T) {
	ctrl := &fakeController{snaps: []serve.Snapshot{{ID: 3, Status: serve.StatusDownloading}}}
	m := New(ctrl, NewLogBuffer(10), ".")
	m.Update(key("s"))
	if ctrl.paused != 1 {
		t.Fatal("下载中的任务按 s 应调用暂停")
	}

	ctrl.snaps = []serve.Snapshot{{ID: 3, Status: serve.StatusPaused}}
	m.Update(key("s"))
	if ctrl.resumed != 1 {
		t.Fatal("已暂停的任务按 s 应调用恢复")
	}
}

func TestLogBufferKeepsRecentLines(t *testing.T) {
	b := NewLogBuffer(2)
	if _, err := b.Write([]byte("a\nb\nc\n")); err != nil {
		t.Fatal(err)
	}
	lines := b.Lines()
	if len(lines) != 2 || lines[0] != "b" || lines[1] != "c" {
		t.Fatalf("日志缓冲只应保留最后两行, 实际为 %v", lines)
	}
}
