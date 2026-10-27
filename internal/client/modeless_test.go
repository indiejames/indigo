package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// modelessModel is a buffer able to make edits: a zero RPC gives applyOp a
// client id, and nothing here runs the commands that would reach a server.
func modelessModel(content string) Model {
	m := newTestModel(content)
	m.rpc = &RPC{}
	m.filePath = "test.go"
	return m
}

// press sends one key through Update, the path a real key takes.
func pressKey(t *testing.T, m Model, k tea.KeyPressMsg) (Model, tea.Cmd) {
	t.Helper()
	out, cmd := m.Update(k)
	return out.(Model), cmd
}

func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m, _ = pressKey(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func ctrlKey(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

var (
	ctrlZ     = ctrlKey('z')
	ctrlSpace = tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl}
	selectAll = tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModShift}
	leftKey   = tea.KeyPressMsg{Code: tea.KeyLeft}
	escKey    = tea.KeyPressMsg{Code: tea.KeyEscape}
	bsKey     = tea.KeyPressMsg{Code: tea.KeyBackspace}
)

// fakeClock makes typing pauses controllable.
func fakeClock(t *testing.T) *time.Time {
	t.Helper()
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = old })
	return &at
}

func TestTheEditorStartsReadyToType(t *testing.T) {
	m := typeText(t, modelessModel("\n"), "hi")
	if got := m.buf.Content(); got != "hi\n" {
		t.Errorf("content = %q, want %q", got, "hi\n")
	}
	if m.mode != ModeInsert {
		t.Errorf("mode = %v, want ModeInsert", m.mode)
	}
}

// TestATypingRunIsOneUndoStep: with no Esc to end an insert session, a run of
// typing is what one undo takes back.
func TestATypingRunIsOneUndoStep(t *testing.T) {
	fakeClock(t)
	m := typeText(t, modelessModel("\n"), "abc")
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "\n" {
		t.Errorf("after undo content = %q, want the run undone in one step", got)
	}
}

func TestAPauseEndsATypingRun(t *testing.T) {
	clock := fakeClock(t)
	m := typeText(t, modelessModel("\n"), "ab")
	*clock = clock.Add(2 * typingPause)
	m = typeText(t, m, "cd")
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "ab\n" {
		t.Errorf("after undo content = %q, want only the typing after the pause undone", got)
	}
}

// TestACommandEndsATypingRun: moving the cursor is a command, so the typing
// after it is a separate undo step.
func TestACommandEndsATypingRun(t *testing.T) {
	fakeClock(t)
	m := typeText(t, modelessModel("\n"), "ab")
	m, _ = pressKey(t, m, leftKey)
	m = typeText(t, m, "X")
	if got := m.buf.Content(); got != "aXb\n" {
		t.Fatalf("content = %q, want %q", got, "aXb\n")
	}
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "ab\n" {
		t.Errorf("after one undo content = %q, want %q", got, "ab\n")
	}
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "\n" {
		t.Errorf("after two undos content = %q, want %q", got, "\n")
	}
}

func TestTypingReplacesTheSelection(t *testing.T) {
	m := modelessModel("hello\n")
	m, _ = pressKey(t, m, selectAll)
	m = typeText(t, m, "x")
	if got := m.buf.Content(); got != "x" {
		t.Errorf("content = %q, want the selection replaced by %q", got, "x")
	}
}

func TestBackspaceDeletesOnlyTheSelection(t *testing.T) {
	m := modelessModel("one two\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 4}, Head: document.Pos{Line: 0, Col: 7}} // "two"
	m.cursor = m.sel.Head
	m, _ = pressKey(t, m, bsKey)
	if got := m.buf.Content(); got != "one \n" {
		t.Errorf("content = %q, want %q", got, "one \n")
	}
}

func TestEscClearsTheSelectionAndKeepsTyping(t *testing.T) {
	m := modelessModel("abc\n")
	m, _ = pressKey(t, m, selectAll)
	m, _ = pressKey(t, m, escKey)
	if m.sel != nil {
		t.Error("selection survived Esc")
	}
	if m.mode != ModeInsert {
		t.Errorf("mode = %v after Esc, want ModeInsert", m.mode)
	}
}

func TestCtrlWAsksBeforeClosingAnUnsavedBuffer(t *testing.T) {
	m := modelessModel("abc\n")
	m.buf.MarkDirty()
	_, cmd := pressKey(t, m, ctrlKey('w'))
	found := false
	for _, msg := range collectMsgs(cmd) {
		if _, ok := msg.(ConfirmCloseMsg); ok {
			found = true
		}
	}
	if !found {
		t.Error("Ctrl+W on an unsaved buffer did not ask the App to confirm")
	}
}

func TestCtrlQAsksTheAppToQuit(t *testing.T) {
	_, cmd := pressKey(t, modelessModel("abc\n"), ctrlKey('q'))
	found := false
	for _, msg := range collectMsgs(cmd) {
		if _, ok := msg.(QuitRequestMsg); ok {
			found = true
		}
	}
	if !found {
		t.Error("Ctrl+Q sent no QuitRequestMsg")
	}
}

// TestCtrlSpaceOpensTheCommandMenu: Ctrl+Space then g g is the Space menu's Go
// menu, go to top.
func TestCtrlSpaceOpensTheCommandMenu(t *testing.T) {
	m := modelessModel("one\ntwo\nthree\n")
	m.cursor = document.Pos{Line: 2, Col: 1}
	m, _ = pressKey(t, m, ctrlSpace)
	m = typeText(t, m, "gg")
	if m.cursor != (document.Pos{}) {
		t.Errorf("cursor = %+v, want the top of the file", m.cursor)
	}
	if got := m.buf.Content(); got != "one\ntwo\nthree\n" {
		t.Errorf("the menu keys were typed: content = %q", got)
	}
}

func TestThePaletteFindsAnActionByLabel(t *testing.T) {
	found := false
	for _, c := range filteredCmds("Select all") {
		if c.name == "select-all" {
			found = true
		}
	}
	if !found {
		t.Error("the palette does not list select-all for \"Select all\"")
	}
}

func TestThePaletteRunsAnActionByName(t *testing.T) {
	m := modelessModel("abc\n")
	m.mode = ModeCommand
	m.cmdBuf = "select-all"
	out, _ := m.executeCommand()
	got := out.(Model)
	if got.sel == nil {
		t.Error("running select-all from the palette selected nothing")
	}
	if got.mode != ModeInsert {
		t.Errorf("mode = %v after the palette, want ModeInsert", got.mode)
	}
}

func TestCtrlCCopiesTheLineWhenNothingIsSelected(t *testing.T) {
	fakeClipboardContent = ""
	m := modelessModel("first\nsecond\n")
	m.cursor = document.Pos{Line: 1, Col: 2}
	pressKey(t, m, ctrlKey('c'))
	if fakeClipboardContent != "second\n" {
		t.Errorf("clipboard = %q, want the whole line", fakeClipboardContent)
	}
}

func TestOptionArrowMovesByWord(t *testing.T) {
	m := modelessModel("alpha beta\n")
	// Ghostty sends Option+→ as alt+f.
	m, _ = pressKey(t, m, tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt})
	if m.cursor.Col != 5 {
		t.Errorf("cursor col = %d after Option+→, want 5 (end of alpha)", m.cursor.Col)
	}
}

// TestAClickEndsATypingRun: typing, clicking somewhere else and typing again
// within the pause is two undo steps, not one that spans both places.
func TestAClickEndsATypingRun(t *testing.T) {
	fakeClock(t)
	m := typeText(t, modelessModel("one\ntwo\n"), "ab")
	out, _ := m.Update(tea.MouseClickMsg{X: 3, Y: 1, Button: tea.MouseLeft})
	m = out.(Model)
	m = typeText(t, m, "cd")
	if got := m.buf.Content(); got != "abone\ntwocd\n" {
		t.Fatalf("content = %q, want %q", got, "abone\ntwocd\n")
	}
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "abone\ntwo\n" {
		t.Errorf("after one undo content = %q, want only the typing after the click undone", got)
	}
}

// TestCtrlQuestionMarkTogglesTheKeyBindings: Ctrl+? shows the key bindings, as
// ? did in Normal mode, and closes them again. It arrives as ctrl+shift+/ from
// a terminal reporting keys the kitty way and as ctrl+? from others, and help
// lists it once, as printed on the key.
func TestCtrlQuestionMarkTogglesTheKeyBindings(t *testing.T) {
	for name, k := range map[string]tea.KeyPressMsg{
		"ctrl+shift+/": {Code: '/', Mod: tea.ModCtrl | tea.ModShift},
		"ctrl+?":       {Code: '?', Mod: tea.ModCtrl},
	} {
		t.Run(name, func(t *testing.T) {
			if k.String() != name {
				t.Fatalf("key reads %q, want %q", k.String(), name)
			}
			m, _ := pressKey(t, modelessModel("hello\n"), k)
			if !m.helpVisible {
				t.Fatal("the key didn't open the key bindings")
			}
			m, _ = pressKey(t, m, k)
			if m.helpVisible {
				t.Error("pressing it again didn't close them")
			}
			if got := m.buf.Content(); got != "hello\n" {
				t.Errorf("content = %q, want nothing typed", got)
			}
		})
	}

	for _, e := range generateHelpEntries() {
		if e.desc != "Show key bindings" {
			continue
		}
		if strings.Count(e.key, "Ctrl+?") != 1 || strings.Contains(e.key, "Shift+/") {
			t.Errorf("help lists the key as %q, want Ctrl+? once", e.key)
		}
		return
	}
	t.Error("help has no Show key bindings row")
}

// TestTheMatchMenuIsInTheCommandMenu: the modal editor's m menu (text objects
// and the matching bracket) is Ctrl+Space m, with the same keys after it. Move Function to File, which
// m used to open there, moved to M.
func TestTheMatchMenuIsInTheCommandMenu(t *testing.T) {
	const content = "f(bar, baz)\n"
	cases := []struct {
		keys string
		want string
	}{
		{"miw", "bar"},
		{"mim", "bar, baz"},
		{"mam", "(bar, baz)"},
	}
	for _, c := range cases {
		m := modelessModel(content)
		m.cursor = document.Pos{Line: 0, Col: 3}
		m, _ = pressKey(t, m, ctrlSpace)
		m = typeText(t, m, c.keys)
		if got := m.buf.Content(); got != content {
			t.Fatalf("%s: the menu keys were typed: content = %q", c.keys, got)
		}
		if m.sel == nil {
			t.Fatalf("%s: nothing selected", c.keys)
		}
		if got := m.textForSelection(m.sel); got != c.want {
			t.Errorf("%s selected %q, want %q", c.keys, got, c.want)
		}
	}

	m := modelessModel(content)
	m.cursor = document.Pos{Line: 0, Col: 1}
	m, _ = pressKey(t, m, ctrlSpace)
	m = typeText(t, m, "mm")
	if want := (document.Pos{Line: 0, Col: 10}); m.cursor != want {
		t.Errorf("mm: cursor = %+v, want the matching bracket at %+v", m.cursor, want)
	}

	m = modelessModel(content)
	m, _ = pressKey(t, m, ctrlSpace)
	m = typeText(t, m, "M")
	if m.mode != ModeCommand || m.cmdBuf != "move-to-file " {
		t.Errorf("M: mode = %v, command line = %q, want the move-to-file prompt", m.mode, m.cmdBuf)
	}
}

// TestTypingReplacesEveryCursorsSelection: Ctrl+D selects each occurrence, and
// typing replaces all of them, in one undo step.
func TestTypingReplacesEveryCursorsSelection(t *testing.T) {
	m := modelessModel("foo bar foo baz\n")
	m.cursor = document.Pos{Line: 0, Col: 1}
	m, _ = pressKey(t, m, ctrlKey('d'))
	m, _ = pressKey(t, m, ctrlKey('d'))
	m = typeText(t, m, "xy")
	if got := m.buf.Content(); got != "xy bar xy baz\n" {
		t.Fatalf("content = %q, want both foos replaced", got)
	}
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "foo bar foo baz\n" {
		t.Errorf("after undo content = %q, want the original", got)
	}
}

// TestBackspaceDeletesEveryCursorsSelection: with selections, Backspace
// deletes them and nothing more.
func TestBackspaceDeletesEveryCursorsSelection(t *testing.T) {
	m := modelessModel("foo bar foo\n")
	m.cursor = document.Pos{Line: 0, Col: 1}
	m, _ = pressKey(t, m, ctrlKey('d'))
	m, _ = pressKey(t, m, ctrlKey('d'))
	m, _ = pressKey(t, m, bsKey)
	if got := m.buf.Content(); got != " bar \n" {
		t.Errorf("content = %q, want both foos deleted and nothing else", got)
	}
}

// TestTypingAtBareCursorsDeletesNothing: cursors without a selection just
// type, even when another cursor has one.
func TestTypingAtBareCursorsDeletesNothing(t *testing.T) {
	m := modelessModel("abc\nabc\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 1}}
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 1}, goalCol: -1}}
	m = typeText(t, m, "X")
	if got := m.buf.Content(); got != "Xbc\naXbc\n" {
		t.Errorf("content = %q, want the selection replaced and the bare cursor's text kept", got)
	}
}

// TestPastingReplacesEveryCursorsSelection: Ctrl+V over Ctrl+D's selections
// replaces each of them, in one undo step.
func TestPastingReplacesEveryCursorsSelection(t *testing.T) {
	m := modelessModel("foo bar foo\n")
	withFakeClipboard(t, "zz")
	m.cursor = document.Pos{Line: 0, Col: 1}
	m, _ = pressKey(t, m, ctrlKey('d'))
	m, _ = pressKey(t, m, ctrlKey('d'))
	m, _ = pressKey(t, m, ctrlKey('v'))
	if got := m.buf.Content(); got != "zz bar zz\n" {
		t.Fatalf("content = %q, want both foos replaced", got)
	}
	m, _ = pressKey(t, m, ctrlZ)
	if got := m.buf.Content(); got != "foo bar foo\n" {
		t.Errorf("after undo content = %q, want the original", got)
	}
}
