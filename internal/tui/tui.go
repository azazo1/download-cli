// Package tui 提供 dn serve 的终端界面: 展示任务列表与日志, 并支持暂停, 恢复, 移除.
package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/dustin/go-humanize"

	"github.com/azazo1/download-cli/internal/serve"
)

const (
	// tickInterval 是界面刷新间隔. 下载进度靠轮询快照获得.
	tickInterval = 300 * time.Millisecond
	maxTaskRows  = 8
	// maxLogLines 是日志缓冲区保留的行数.
	maxLogLines = 500
)

// Controller 是界面操作的服务的抽象, 由 serve.Server 实现.
type Controller interface {
	Address() string
	Snapshots() []serve.Snapshot
	Pause(id int) error
	Resume(id int) error
	Remove(id int) error
}

// LogBuffer 收集日志行并在界面上展示. 它实现了 io.Writer, 可以直接作为日志输出目标.
type LogBuffer struct {
	mu      sync.Mutex
	lines   []string
	pending string
	max     int
}

// NewLogBuffer 创建最多保留 max 行的日志缓冲.
func NewLogBuffer(max int) *LogBuffer {
	if max <= 0 {
		max = maxLogLines
	}
	return &LogBuffer{max: max}
}

// Write 实现 io.Writer.
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending += string(p)
	for {
		i := strings.IndexByte(b.pending, '\n')
		if i < 0 {
			break
		}
		b.lines = append(b.lines, strings.TrimRight(b.pending[:i], "\r"))
		b.pending = b.pending[i+1:]
	}
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	return len(p), nil
}

// Lines 返回日志行的副本.
func (b *LogBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	helpStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	noteStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	pausedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	barFilled    = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	barEmpty     = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

type sample struct {
	at   time.Time
	done int64
}

// Model 是界面状态. 它只读服务快照, 并通过 Controller 下发操作.
type Model struct {
	ctrl Controller
	logs *LogBuffer
	dir  string

	width, height int
	selected      int
	quitArm       bool
	note          string
	noteAt        time.Time

	lastSpeed map[int]float64
	samples   map[int]sample
}

// New 构造界面模型.
func New(ctrl Controller, logs *LogBuffer, dir string) *Model {
	return &Model{
		ctrl:      ctrl,
		logs:      logs,
		dir:       dir,
		lastSpeed: map[int]float64{},
		samples:   map[int]sample{},
	}
}

type tickMsg time.Time

// Init 实现 tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.tick()
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update 实现 tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		m.sampleSpeeds()
		return m, m.tick()
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	snaps := m.ctrl.Snapshots()
	switch msg.String() {
	case "ctrl+c", "q":
		if m.hasRunning(snaps) && !m.quitArm {
			m.quitArm = true
			m.setNote("还有任务在下载, 再按一次 q 退出 (进度会保存, 下次可继续)")
			return m, nil
		}
		return m, tea.Quit
	case "esc":
		m.quitArm = false
		m.note = ""
		return m, nil
	case "up", "k":
		m.selectBy(snaps, -1)
	case "down", "j":
		m.selectBy(snaps, 1)
	case "s", " ":
		m.toggle(snaps)
	case "r":
		m.resume(snaps)
	case "x":
		m.remove(snaps)
	}
	m.quitArm = false
	return m, nil
}

// hasRunning 判断是否有任务正在下载.
func (m *Model) hasRunning(snaps []serve.Snapshot) bool {
	for _, s := range snaps {
		if s.Status == serve.StatusDownloading {
			return true
		}
	}
	return false
}

// selectBy 按 -1 / +1 移动选中项.
func (m *Model) selectBy(snaps []serve.Snapshot, delta int) {
	if len(snaps) == 0 {
		m.selected = 0
		return
	}
	idx := m.selectedIndex(snaps)
	idx += delta
	if idx < 0 {
		idx = 0
	}
	if idx >= len(snaps) {
		idx = len(snaps) - 1
	}
	m.selected = snaps[idx].ID
}

// selectedIndex 返回当前选中任务在快照列表中的下标, 找不到时返回 -1.
func (m *Model) selectedIndex(snaps []serve.Snapshot) int {
	if m.selected == 0 && len(snaps) > 0 {
		m.selected = snaps[0].ID
	}
	for i, s := range snaps {
		if s.ID == m.selected {
			return i
		}
	}
	if len(snaps) > 0 {
		m.selected = snaps[0].ID
		return 0
	}
	return -1
}

func (m *Model) current(snaps []serve.Snapshot) (serve.Snapshot, bool) {
	idx := m.selectedIndex(snaps)
	if idx < 0 {
		return serve.Snapshot{}, false
	}
	return snaps[idx], true
}

// toggle 暂停或恢复选中任务.
func (m *Model) toggle(snaps []serve.Snapshot) {
	cur, ok := m.current(snaps)
	if !ok {
		return
	}
	var err error
	if cur.Status == serve.StatusDownloading {
		err = m.ctrl.Pause(cur.ID)
	} else {
		err = m.ctrl.Resume(cur.ID)
	}
	m.report(err)
}

func (m *Model) resume(snaps []serve.Snapshot) {
	cur, ok := m.current(snaps)
	if !ok {
		return
	}
	m.report(m.ctrl.Resume(cur.ID))
}

func (m *Model) remove(snaps []serve.Snapshot) {
	cur, ok := m.current(snaps)
	if !ok {
		return
	}
	m.report(m.ctrl.Remove(cur.ID))
}

func (m *Model) report(err error) {
	if err != nil {
		m.note = err.Error()
	} else {
		m.note = ""
	}
	m.noteAt = time.Time{}
}

func (m *Model) setNote(text string) {
	m.note = text
	m.noteAt = time.Now()
}

// sampleSpeeds 根据两次快照之间的增量估算每个任务的瞬时速度.
func (m *Model) sampleSpeeds() {
	now := time.Now()
	live := map[int]bool{}
	for _, s := range m.ctrl.Snapshots() {
		if s.Status != serve.StatusDownloading {
			delete(m.samples, s.ID)
			delete(m.lastSpeed, s.ID)
			continue
		}
		live[s.ID] = true
		prev, ok := m.samples[s.ID]
		if ok {
			dt := now.Sub(prev.at).Seconds()
			if dt > 0.05 {
				m.lastSpeed[s.ID] = float64(max(s.Downloaded-prev.done, 0)) / dt
			}
		}
		m.samples[s.ID] = sample{at: now, done: s.Downloaded}
	}
	for id := range m.samples {
		if !live[id] {
			delete(m.samples, id)
		}
	}
}

// View 实现 tea.Model.
func (m *Model) View() string {
	snaps := m.ctrl.Snapshots()
	width := m.width
	if width <= 0 {
		width = 100
	}
	height := m.height
	if height <= 0 {
		height = 30
	}

	var b strings.Builder
	// 头部: 服务地址与下载目录.
	running := 0
	for _, s := range snaps {
		if s.Status == serve.StatusDownloading {
			running++
		}
	}
	b.WriteString(titleStyle.Render("dn serve"))
	b.WriteString(dimStyle.Render("  " + m.ctrl.Address()))
	b.WriteString(dimStyle.Render("  目录 " + m.dir))
	b.WriteString(dimStyle.Render(fmt.Sprintf("  任务 %d (下载中 %d)", len(snaps), running)))
	b.WriteString("\n\n")

	rows := m.taskRows(snaps, height)
	if len(snaps) == 0 {
		b.WriteString(dimStyle.Render("  还没有任务. 在浏览器里点击下载, 扩展会把它发给这里."))
		b.WriteString("\n")
	} else {
		b.WriteString(m.taskHeader(width))
		b.WriteString("\n")
		m.renderTasks(&b, snaps, rows)
	}

	// 日志区占剩余高度.
	used := 3 + 1 + max(rows, 1) + 1
	logLines := height - used - 3
	if logLines < 0 {
		logLines = 0
	}
	if logLines > 0 {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("日志"))
		b.WriteString("\n")
		lines := m.logs.Lines()
		if len(lines) > logLines {
			lines = lines[len(lines)-logLines:]
		}
		for _, line := range lines {
			b.WriteString(truncate(line, width))
			b.WriteString("\n")
		}
	}

	if m.note != "" {
		style := noteStyle
		if m.noteAt.IsZero() {
			style = errorStyle
		}
		b.WriteString(style.Render(truncate(m.note, width)))
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render("↑/↓ 选择  s 暂停/恢复  r 重试  x 移除  q 退出"))
	return b.String()
}

// taskRows 计算任务区显示多少行.
func (m *Model) taskRows(snaps []serve.Snapshot, height int) int {
	rows := min(len(snaps), maxTaskRows)
	if rows == 0 {
		return 0
	}
	if height < 16 {
		rows = min(rows, 3)
	}
	return rows
}

func (m *Model) taskHeader(width int) string {
	line := fmt.Sprintf("  %-3s %-28s %-24s %10s %8s  %s", "#", "名称", "进度", "速度", "连接", "状态")
	return dimStyle.Render(truncate(line, width))
}

// renderTasks 画出任务行, 任务很多时按选中项滚动.
func (m *Model) renderTasks(b *strings.Builder, snaps []serve.Snapshot, rows int) {
	idx := m.selectedIndex(snaps)
	start := 0
	if idx >= rows {
		start = idx - rows + 1
	}
	if start+rows > len(snaps) {
		start = max(len(snaps)-rows, 0)
	}
	for i := start; i < start+rows && i < len(snaps); i++ {
		s := snaps[i]
		line := m.taskLine(s)
		if s.ID == m.selected {
			// 不用整行反显: 行内已经有状态颜色, 嵌套样式会被重置打断.
			line = "> " + strings.TrimPrefix(line, "  ")
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
}

// taskLine 渲染单个任务行.
func (m *Model) taskLine(s serve.Snapshot) string {
	name := truncate(s.Name, 28)
	progress := m.progressText(s)
	speed := "—"
	if s.Status == serve.StatusDownloading {
		if v, ok := m.lastSpeed[s.ID]; ok {
			speed = humanize.IBytes(uint64(max(int64(v), 0))) + "/s"
		}
	}
	status := statusStyle(s.Status).Render(s.Status.String())
	if s.Status == serve.StatusFailed && s.Error != "" {
		status = errorStyle.Render("失败: " + truncate(s.Error, 24))
	}
	conns := "—"
	if s.Status == serve.StatusDownloading {
		conns = fmt.Sprintf("%d", s.Active)
	}
	line := fmt.Sprintf("  %-3d %-28s %-24s %10s %8s  %s", s.ID, name, progress, speed, conns, status)
	return line
}

// progressText 生成进度条文本.
func (m *Model) progressText(s serve.Snapshot) string {
	const barWidth = 10
	if s.Total <= 0 {
		if s.Downloaded > 0 {
			return humanize.IBytes(uint64(s.Downloaded)) + "  " + barEmpty.Render(strings.Repeat("░", barWidth))
		}
		return "探测中  " + barEmpty.Render(strings.Repeat("░", barWidth))
	}
	ratio := float64(s.Downloaded) / float64(s.Total)
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * barWidth)
	bar := barFilled.Render(strings.Repeat("█", filled)) + barEmpty.Render(strings.Repeat("░", barWidth-filled))
	return fmt.Sprintf("%5.1f%% %s", ratio*100, bar)
}

// statusStyle 返回状态对应的样式.
func statusStyle(s serve.Status) lipgloss.Style {
	switch s {
	case serve.StatusDownloading:
		return runningStyle
	case serve.StatusDone:
		return doneStyle
	case serve.StatusPaused:
		return pausedStyle
	default:
		return errorStyle
	}
}

// truncate 按显示宽度截断字符串, 超出时补省略号.
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(text)
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		if lipgloss.Width(string(runes)) <= width-1 {
			return string(runes) + "…"
		}
	}
	return "…"
}
