package client

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// fakeKey creates a KeyMsg that matches the given string representation,
// mirroring how BubbleTea dispatches key events to handlers.
func fakeKey(s string) tea.KeyMsg {
	switch s {
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "shift+up":
		return tea.KeyMsg{Type: tea.KeyShiftUp}
	case "shift+down":
		return tea.KeyMsg{Type: tea.KeyShiftDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// newTestModel creates a minimal Model suitable for unit tests that don't
// invoke any RPC methods. The returned tea.Cmd from executeCommand is safe to
// discard — RPC calls are always deferred inside the returned func.
func newTestModel(content string) Model {
	return Model{
		buf:     document.New("test.go", content),
		metrics: &metricsData{},
		height:  24,
		width:   80,
	}
}

// TestExecuteCommandGotoLine verifies numeric commands move the cursor.
func TestExecuteCommandGotoLine(t *testing.T) {
	m := newTestModel("line1\nline2\nline3\n")

	m.cmdBuf = "2"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if got.cursor.Line != 1 {
		t.Errorf("goto 2: cursor.Line = %d, want 1", got.cursor.Line)
	}
	if got.mode != ModeNormal {
		t.Errorf("goto 2: mode = %v, want ModeNormal", got.mode)
	}
	if got.cmdBuf != "" {
		t.Errorf("goto 2: cmdBuf = %q, want empty", got.cmdBuf)
	}
}

func TestExecuteCommandGotoLineOutOfRange(t *testing.T) {
	m := newTestModel("line1\nline2\n")

	m.cmdBuf = "99"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if got.status == "" {
		t.Error("out-of-range goto should set status error")
	}
}

func TestExecuteCommandMetricsToggle(t *testing.T) {
	m := newTestModel("")

	m.cmdBuf = "metrics"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if !got.metrics.show {
		t.Error("first :metrics should enable overlay")
	}

	got.cmdBuf = "metrics"
	m3, _ := got.executeCommand()
	got2 := m3.(Model)
	if got2.metrics.show {
		t.Error("second :metrics should disable overlay")
	}
}

func TestExecuteCommandUnknown(t *testing.T) {
	m := newTestModel("")
	m.cmdBuf = "nosuchcommand"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if got.status == "" {
		t.Error("unknown command should set status error")
	}
	if got.mode != ModeNormal {
		t.Errorf("unknown command: mode = %v, want ModeNormal", got.mode)
	}
}

func TestExecuteCommandQuitClean(t *testing.T) {
	m := newTestModel("unchanged")
	m.cmdBuf = "quit"
	_, cmd := m.executeCommand()
	if cmd == nil {
		t.Error("quit on clean buffer should return a close cmd")
	}
}

func TestExecuteCommandQuitDirty(t *testing.T) {
	m := newTestModel("text")
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 4, InsertText: "!"})

	m.cmdBuf = "quit"
	m2, cmd := m.executeCommand()
	got := m2.(Model)
	if cmd != nil {
		t.Error("quit on dirty buffer should not return a close cmd")
	}
	if got.status == "" {
		t.Error("quit on dirty buffer should set error status")
	}
}

func TestExecuteCommandForceQuit(t *testing.T) {
	m := newTestModel("text")
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 4, InsertText: "!"})

	for _, name := range []string{"q!", "quit!"} {
		m.cmdBuf = name
		_, cmd := m.executeCommand()
		if cmd == nil {
			t.Errorf("%q: expected a close cmd", name)
		}
	}
}

func TestExecuteCommandSaveAliases(t *testing.T) {
	m := newTestModel("text")
	for _, cmd := range []string{"w", "write", "s", "save"} {
		m.cmdBuf = cmd
		m2, _ := m.executeCommand()
		got := m2.(Model)
		// Cmd is a tea.Cmd func that calls RPC — we just verify state is clean.
		if got.mode != ModeNormal {
			t.Errorf("%q: mode = %v, want ModeNormal", cmd, got.mode)
		}
		if got.cmdBuf != "" {
			t.Errorf("%q: cmdBuf not cleared", cmd)
		}
	}
}

func TestExecuteCommandWriteQuitAliases(t *testing.T) {
	m := newTestModel("text")
	for _, cmd := range []string{"wq", "wqa", "x", "write-quit"} {
		m.cmdBuf = cmd
		m2, _ := m.executeCommand()
		got := m2.(Model)
		if got.mode != ModeNormal {
			t.Errorf("%q: mode = %v, want ModeNormal", cmd, got.mode)
		}
	}
}

// TestHandleCommandTyping checks that characters accumulate in cmdBuf.
func TestHandleCommandTyping(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand

	for _, ch := range "quit" {
		msg := fakeKey(string(ch))
		m2, _ := m.handleCommand(msg)
		m = m2.(Model)
	}
	if m.cmdBuf != "quit" {
		t.Errorf("cmdBuf = %q, want %q", m.cmdBuf, "quit")
	}
}

func TestHandleCommandBackspace(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "qui"

	m2, _ := m.handleCommand(fakeKey("backspace"))
	got := m2.(Model)
	if got.cmdBuf != "qu" {
		t.Errorf("after backspace: cmdBuf = %q, want %q", got.cmdBuf, "qu")
	}
}

func TestHandleCommandBackspaceEmpty(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = ""

	m2, _ := m.handleCommand(fakeKey("backspace"))
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("backspace on empty buf: mode = %v, want ModeNormal", got.mode)
	}
}

func TestHandleCommandEsc(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "something"

	m2, _ := m.handleCommand(fakeKey("esc"))
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("esc: mode = %v, want ModeNormal", got.mode)
	}
	if got.cmdBuf != "" {
		t.Errorf("esc: cmdBuf = %q, want empty", got.cmdBuf)
	}
}


// TestPushStatusLogsMessage verifies pushStatus records non-empty messages
// in messageLog and classifies "E:"/"ERR:"-prefixed ones as errors.
func TestPushStatusLogsMessage(t *testing.T) {
	m := newTestModel("")
	m = m.pushStatus("copied")
	m = m.pushStatus("E: pattern not found")

	if m.status != "E: pattern not found" {
		t.Errorf("status = %q, want %q", m.status, "E: pattern not found")
	}
	if len(m.messageLog) != 2 {
		t.Fatalf("len(messageLog) = %d, want 2", len(m.messageLog))
	}
	if m.messageLog[0].text != "copied" || m.messageLog[0].isErr {
		t.Errorf("messageLog[0] = %+v, want text=copied isErr=false", m.messageLog[0])
	}
	if m.messageLog[1].text != "E: pattern not found" || !m.messageLog[1].isErr {
		t.Errorf("messageLog[1] = %+v, want text=E:... isErr=true", m.messageLog[1])
	}
}

// TestPushStatusClearDoesNotLog verifies clearing the status (empty string)
// doesn't add a spurious entry to messageLog.
func TestPushStatusClearDoesNotLog(t *testing.T) {
	m := newTestModel("")
	m = m.pushStatus("copied")
	m = m.pushStatus("")

	if m.status != "" {
		t.Errorf("status = %q, want empty", m.status)
	}
	if len(m.messageLog) != 1 {
		t.Fatalf("len(messageLog) = %d, want 1", len(m.messageLog))
	}
}

// TestPushStatusCapsLog verifies messageLog never grows past maxMessageLog,
// keeping the most recent entries.
func TestPushStatusCapsLog(t *testing.T) {
	m := newTestModel("")
	for i := 0; i < maxMessageLog+10; i++ {
		m = m.pushStatus("msg")
	}
	if len(m.messageLog) != maxMessageLog {
		t.Fatalf("len(messageLog) = %d, want %d", len(m.messageLog), maxMessageLog)
	}
}

// TestMessageLogPopupOpenScrollClose verifies the space-l menu action opens
// the popup, j/k scroll it, and esc/q close it without leaking scroll state.
func TestMessageLogPopupOpenScrollClose(t *testing.T) {
	m := newTestModel("")
	for i := 0; i < 5; i++ {
		m = m.pushStatus("msg")
	}

	m2, _ := findCommand([]string{" ", "l"})
	if m2 == nil || m2.execute == nil {
		t.Fatal("space-l command not found in commandMenuRoot")
	}
	res, _ := m2.execute(m)
	m = res.(Model)
	if !m.msgLogVisible {
		t.Fatal("space-l did not open the message log popup")
	}

	res, _ = m.handleKey(fakeKey("k"))
	m = res.(Model)
	if !m.msgLogVisible {
		t.Error("k should scroll, not close, the popup")
	}

	res, _ = m.handleKey(fakeKey("esc"))
	m = res.(Model)
	if m.msgLogVisible {
		t.Error("esc did not close the message log popup")
	}
	if m.msgLogScroll != 0 {
		t.Errorf("msgLogScroll after close = %d, want 0", m.msgLogScroll)
	}
}

// TestMessageLogPopupOpensScrolledToLastPage verifies opening the popup with
// enough entries to overflow one page starts scrolled to the most recent
// messages (not a raw sentinel that takes many "k" presses to unstick).
func TestMessageLogPopupOpensScrolledToLastPage(t *testing.T) {
	m := newTestModel("")
	for i := 0; i < 200; i++ {
		m = m.pushStatus("msg")
	}

	cmd, _ := findCommand([]string{" ", "l"})
	if cmd == nil || cmd.execute == nil {
		t.Fatal("space-l command not found in commandMenuRoot")
	}
	res, _ := cmd.execute(m)
	m = res.(Model)

	maxScroll := messageLogMaxScroll(m.width, m.height, m.messageLog)
	if maxScroll == 0 {
		t.Fatal("test setup: expected enough entries to require scrolling")
	}
	if m.msgLogScroll != maxScroll {
		t.Fatalf("msgLogScroll after open = %d, want %d (last page)", m.msgLogScroll, maxScroll)
	}

	res, _ = m.handleKey(fakeKey("k"))
	m = res.(Model)
	if m.msgLogScroll != maxScroll-1 {
		t.Errorf("msgLogScroll after one k = %d, want %d (one line up from the last page)", m.msgLogScroll, maxScroll-1)
	}
}
