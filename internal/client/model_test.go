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
	// Buffer starts clean.
	m.cmdBuf = "quit"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if !got.quitting {
		t.Error("quit on clean buffer should set quitting")
	}
}

func TestExecuteCommandQuitDirty(t *testing.T) {
	m := newTestModel("text")
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 4, InsertText: "!"})

	m.cmdBuf = "quit"
	m2, _ := m.executeCommand()
	got := m2.(Model)
	if got.quitting {
		t.Error("quit on dirty buffer should not quit")
	}
	if got.status == "" {
		t.Error("quit on dirty buffer should set error status")
	}
}

func TestExecuteCommandForceQuit(t *testing.T) {
	m := newTestModel("text")
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 4, InsertText: "!"})

	for _, cmd := range []string{"q!", "quit!"} {
		m.cmdBuf = cmd
		m2, _ := m.executeCommand()
		got := m2.(Model)
		if !got.quitting {
			t.Errorf("%q: expected quitting=true", cmd)
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
