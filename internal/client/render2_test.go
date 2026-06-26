package client

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/document"
)

// --- renderLineRunes ---

func TestRenderLineRunesPlain(t *testing.T) {
	var sb strings.Builder
	runes := []rune("hello")
	renderLineRunes(&sb, runes, -1, -1, -1, nil)
	if got := sb.String(); got != "hello" {
		t.Errorf("plain = %q, want %q", got, "hello")
	}
}

func TestRenderLineRunesCursorAtEnd(t *testing.T) {
	var sb strings.Builder
	runes := []rune("hi")
	// curCol == len(runes): cursor rendered as trailing space
	renderLineRunes(&sb, runes, -1, -1, 2, nil)
	got := sb.String()
	if got == "" {
		t.Error("cursor beyond line should append styled space")
	}
}

func TestRenderLineRunesEmpty(t *testing.T) {
	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, -1, -1, -1, nil)
	if sb.String() != "" {
		t.Errorf("empty runes: got %q, want empty", sb.String())
	}
}

// --- overlayRight ---

func TestOverlayRight(t *testing.T) {
	main := strings.Repeat("a", 20)
	popup := "POPUP"
	result := overlayRight(main, popup, 10)
	if !strings.HasSuffix(result, "POPUP") {
		t.Errorf("overlayRight: result does not end with popup: %q", result)
	}
	w := lipgloss.Width(result)
	if w != 15 { // 10 chars + 5 popup chars
		t.Errorf("overlayRight width = %d, want 15", w)
	}
}

func TestOverlayRightPadsShortMain(t *testing.T) {
	main := "ab"
	popup := "POP"
	// popCol=10, main is only 2 chars — should be padded to 10 then popup appended
	result := overlayRight(main, popup, 10)
	w := lipgloss.Width(result)
	if w != 13 {
		t.Errorf("overlayRight padded: width = %d, want 13", w)
	}
}

// --- renderPopupBox ---

func TestRenderPopupBoxBasic(t *testing.T) {
	items := []command{
		{key: 's', label: "Save"},
		{key: 'q', label: "Quit"},
	}
	lines := renderPopupBox("Menu", items, 40)
	if len(lines) < 4 { // top + 2 items + bottom
		t.Errorf("renderPopupBox: got %d lines, want >= 4", len(lines))
	}
}

func TestRenderPopupBoxEmpty(t *testing.T) {
	lines := renderPopupBox("Empty", nil, 40)
	if len(lines) != 2 { // just top + bottom border
		t.Errorf("empty items: got %d lines, want 2", len(lines))
	}
}

func TestRenderPopupBoxLabelTruncation(t *testing.T) {
	items := []command{
		{key: 'x', label: strings.Repeat("A", 100)},
	}
	// Should not panic on very narrow maxW
	lines := renderPopupBox("T", items, 8)
	_ = lines
}

// --- renderMetricsBox ---

func TestRenderMetricsBox(t *testing.T) {
	m := &metricsData{
		renderDuration:     2 * time.Millisecond,
		highlightDuration:  500 * time.Microsecond,
		keyToFrameDuration: 10 * time.Millisecond,
	}
	lines := renderMetricsBox(m)
	if len(lines) != 5 { // top + 3 rows + bottom
		t.Errorf("renderMetricsBox: got %d lines, want 5", len(lines))
	}
}

// --- renderLine ---

func TestRenderLineNormal(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	line := m.renderLine(0)
	if lipgloss.Width(line) != m.width {
		t.Errorf("renderLine width = %d, want %d", lipgloss.Width(line), m.width)
	}
	if !strings.Contains(line, "hello") {
		t.Errorf("renderLine should contain 'hello', got: %q", line)
	}
}

func TestRenderLineTildeForEmptyRows(t *testing.T) {
	m := newTestModel("hello\n")
	m.height = 24
	// Row beyond buffer content should show tilde
	line := m.renderLine(5)
	if !strings.Contains(line, "~") {
		t.Errorf("out-of-buffer row should contain '~', got: %q", line)
	}
}

func TestRenderLineWithTabs(t *testing.T) {
	m := newTestModel("\thello\n")
	line := m.renderLine(0)
	// Tab should expand to spaces
	if strings.Contains(line, "\t") {
		t.Error("renderLine should expand tabs, but raw tab found")
	}
}

// --- renderStatusBar ---

func TestRenderStatusBarNormalMode(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	bar := m.renderStatusBar()
	w := lipgloss.Width(bar)
	if w != m.width {
		t.Errorf("statusBar width = %d, want %d", w, m.width)
	}
	// Should contain mode label
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "NORMAL") {
		t.Errorf("normal mode bar should contain 'NORMAL': %q", stripped)
	}
}

func TestRenderStatusBarInsertMode(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "INSERT") {
		t.Errorf("insert mode bar should contain 'INSERT': %q", stripped)
	}
}

func TestRenderStatusBarCommandMode(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "quit"
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, ":quit") {
		t.Errorf("command mode bar should contain ':quit': %q", stripped)
	}
}

func TestRenderStatusBarDirtyFlag(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "!"})
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "[+]") {
		t.Errorf("dirty buffer bar should contain '[+]': %q", stripped)
	}
}

func TestRenderStatusBarWarnQuit(t *testing.T) {
	m := newTestModel("")
	m.warnQuit = true
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "Unsaved") {
		t.Errorf("warnQuit bar should contain 'Unsaved': %q", stripped)
	}
}

func TestRenderStatusBarZeroWidth(t *testing.T) {
	m := newTestModel("")
	m.width = 0
	bar := m.renderStatusBar()
	if bar != "" {
		t.Errorf("zero-width bar should be empty, got %q", bar)
	}
}

// --- View ---

func TestViewZeroWidth(t *testing.T) {
	m := newTestModel("hello\n")
	m.width = 0
	if got := m.View(); got != "loading…" {
		t.Errorf("View() with zero width = %q, want 'loading…'", got)
	}
}

func TestViewNormal(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	out := m.View()
	if out == "" {
		t.Error("View() should return non-empty output")
	}
	stripped := ansiStrip(out)
	if !strings.Contains(stripped, "hello") {
		t.Errorf("View() should contain 'hello': %q", stripped[:min(len(stripped), 200)])
	}
}

func TestViewCommandModeShowsPopup(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeCommand
	m.cmdBuf = ""
	out := m.View()
	stripped := ansiStrip(out)
	// The popup title "Commands" should appear
	if !strings.Contains(stripped, "Commands") {
		t.Errorf("command mode View should contain popup 'Commands': %q", stripped[:min(len(stripped), 400)])
	}
}

// --- handleKey dispatcher ---

func TestHandleKeyDispatchesToNormal(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeNormal
	m2, _ := m.handleKey(fakeKey("j"))
	got := m2.(Model)
	if got.cursor.Line != 1 {
		t.Errorf("handleKey normal 'j': cursor.Line = %d, want 1", got.cursor.Line)
	}
}

func TestHandleKeyDispatchesToInsert(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 2
	m2, _ := m.handleKey(fakeKey("esc"))
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("handleKey insert 'esc': mode = %v, want ModeNormal", got.mode)
	}
}

func TestHandleKeyDispatchesToCommand(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "qui"
	m2, _ := m.handleKey(fakeKey("t"))
	got := m2.(Model)
	if got.cmdBuf != "quit" {
		t.Errorf("handleKey command 't': cmdBuf = %q, want 'quit'", got.cmdBuf)
	}
}

func TestHandleKeyClearsStatus(t *testing.T) {
	m := newTestModel("")
	m.status = "some error"
	m.mode = ModeNormal
	m2, _ := m.handleKey(fakeKey("esc"))
	got := m2.(Model)
	if got.status != "" {
		t.Errorf("handleKey should clear status, got %q", got.status)
	}
}

func TestHandleKeyWarnQuitTakesPriority(t *testing.T) {
	m := newTestModel("")
	m.warnQuit = true
	m.mode = ModeNormal
	// When warnQuit is set, handleWarnQuit handles 'n'
	m2, _ := m.handleKey(fakeKey("n"))
	got := m2.(Model)
	if got.warnQuit {
		t.Error("handleKey with warnQuit: 'n' should clear warnQuit")
	}
}

// ansiStrip removes ANSI escape sequences for plain-text assertions.
func ansiStrip(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// still consuming escape
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
