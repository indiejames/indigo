package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// Selections are half-open: positions sit between characters, the cursor is
// the head, and an empty selection is no selection.

var (
	shiftRight  = tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	shiftLeft   = tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	shiftDown   = tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	shiftEnd    = tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModShift}
	optShiftRgt = tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt | tea.ModShift}
	optRight    = tea.KeyPressMsg{Code: 'f', Mod: tea.ModAlt} // what Ghostty sends for Option+→
	plainLeft   = tea.KeyPressMsg{Code: tea.KeyLeft}
	tabKey      = tea.KeyPressMsg{Code: tea.KeyTab}
)

func at(line, col int) document.Pos { return document.Pos{Line: line, Col: col} }

func TestOneShiftRightSelectsOneCharacter(t *testing.T) {
	m, _ := pressKey(t, modelessModel("hello\n"), shiftRight)
	if got := m.selectedText(); got != "h" {
		t.Errorf("selected %q, want %q", got, "h")
	}
	if m.cursor != at(0, 1) {
		t.Errorf("cursor = %+v, want after the selected character", m.cursor)
	}
}

func TestShiftingBackToTheAnchorSelectsNothing(t *testing.T) {
	m, _ := pressKey(t, modelessModel("hello\n"), shiftRight)
	m, _ = pressKey(t, m, shiftLeft)
	if m.sel != nil {
		t.Errorf("sel = %+v, want none: an empty selection is no selection", *m.sel)
	}
}

func TestShiftRightCrossesTheLineBreak(t *testing.T) {
	m := modelessModel("ab\ncd\n")
	m.cursor = at(0, 2)
	m, _ = pressKey(t, m, shiftRight)
	if got := m.selectedText(); got != "\n" {
		t.Errorf("selected %q, want the line break", got)
	}
	if m.cursor != at(1, 0) {
		t.Errorf("cursor = %+v, want the start of the next line", m.cursor)
	}
}

func TestShiftOptionRightSelectsToTheEndOfTheWord(t *testing.T) {
	m, _ := pressKey(t, modelessModel("alpha beta\n"), optShiftRgt)
	if got := m.selectedText(); got != "alpha" {
		t.Errorf("selected %q, want %q", got, "alpha")
	}
}

func TestOptionRightStopsAtTheEndOfTheWord(t *testing.T) {
	m, _ := pressKey(t, modelessModel("alpha beta\n"), optRight)
	if m.cursor != at(0, 5) {
		t.Errorf("cursor = %+v, want the end of alpha", m.cursor)
	}
	m, _ = pressKey(t, m, optRight)
	if m.cursor != at(0, 10) {
		t.Errorf("cursor = %+v, want the end of beta", m.cursor)
	}
}

// TestTypingOverAMultiLineSelection: Shift+↓ selects from the cursor to the
// same column on the next line, and typing replaces exactly that.
func TestTypingOverAMultiLineSelection(t *testing.T) {
	m := modelessModel("one\ntwo\nthree\n")
	m.cursor = at(0, 1)
	m, _ = pressKey(t, m, shiftDown)
	if got := m.selectedText(); got != "ne\nt" {
		t.Fatalf("selected %q, want %q", got, "ne\nt")
	}
	m = typeText(t, m, "X")
	if got := m.buf.Content(); got != "oXwo\nthree\n" {
		t.Errorf("content = %q, want %q", got, "oXwo\nthree\n")
	}
}

func TestShiftEndSelectsToTheLineEndButNotTheBreak(t *testing.T) {
	m := modelessModel("abc\ndef\n")
	m.cursor = at(0, 1)
	m, _ = pressKey(t, m, shiftEnd)
	m = typeText(t, m, "Z")
	if got := m.buf.Content(); got != "aZ\ndef\n" {
		t.Errorf("content = %q, want %q", got, "aZ\ndef\n")
	}
}

func TestLeftCollapsesASelectionToItsStart(t *testing.T) {
	m := modelessModel("abcdef\n")
	m.cursor = at(0, 1)
	for i := 0; i < 3; i++ {
		m, _ = pressKey(t, m, shiftRight)
	}
	m, _ = pressKey(t, m, plainLeft)
	if m.sel != nil || m.cursor != at(0, 1) {
		t.Errorf("sel = %v cursor = %+v, want no selection and the cursor at its start", m.sel, m.cursor)
	}
}

func TestSelectAllCoversTheWholeBuffer(t *testing.T) {
	m, _ := pressKey(t, modelessModel("a\nb\n"), selectAll)
	if got := m.selectedText(); got != "a\nb\n" {
		t.Errorf("selected %q, want the whole buffer", got)
	}
}

// TestASelectionEndingAtColumnZeroLeavesThatLineOut: selecting lines 0 and 1
// by dragging down to the start of line 2 indents only lines 0 and 1, as GUI
// editors do.
func TestASelectionEndingAtColumnZeroLeavesThatLineOut(t *testing.T) {
	m := modelessModel("a\nb\nc\n")
	m.sel = &Selection{Anchor: at(0, 0), Head: at(2, 0)}
	m.cursor = m.sel.Head
	m, _ = pressKey(t, m, tabKey)
	unit := m.indentUnit()
	if got, want := m.buf.Content(), unit+"a\n"+unit+"b\nc\n"; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestDeleteRemovesExactlyTheSelection(t *testing.T) {
	m := modelessModel("abcdef\n")
	m.sel = &Selection{Anchor: at(0, 1), Head: at(0, 3)}
	m.cursor = m.sel.Head
	m, _ = pressKey(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	if got := m.buf.Content(); got != "adef\n" {
		t.Errorf("content = %q, want %q", got, "adef\n")
	}
}

func TestInsideBracketsSelectsTheContents(t *testing.T) {
	m := modelessModel("f(abc)\n")
	m.cursor = at(0, 3)
	out, _ := executeSelectInsideBrackets(m)
	inside := out.(Model)
	if got := inside.selectedText(); got != "abc" {
		t.Errorf("selected %q, want %q", got, "abc")
	}
	m.buf = document.New("test.go", "f()\n")
	m.cursor = at(0, 2)
	out, _ = executeSelectInsideBrackets(m)
	if got := out.(Model); got.sel != nil || got.cursor != at(0, 2) {
		t.Errorf("empty brackets: sel = %v cursor = %+v, want no selection, cursor between them", got.sel, got.cursor)
	}
}

func TestCtrlDSelectsTheWordAndThenTheNextOccurrence(t *testing.T) {
	m := modelessModel("foo bar foo\n")
	m.cursor = at(0, 1)
	m, _ = pressKey(t, m, ctrlKey('d'))
	if got := m.selectedText(); got != "foo" {
		t.Fatalf("selected %q, want %q", got, "foo")
	}
	m, _ = pressKey(t, m, ctrlKey('d'))
	if len(m.extraCursors) != 1 || m.textForSelection(m.extraCursors[0].sel) != "foo" {
		t.Fatalf("extra cursors = %+v, want one on the second foo", m.extraCursors)
	}
	if m.extraCursors[0].pos != at(0, 11) {
		t.Errorf("second cursor at %+v, want after the second foo", m.extraCursors[0].pos)
	}
}

// TestTheReportedSelectionUsesTheProtocolsInclusiveEnd: agents read the
// selection through get_active_context, whose protocol end is inclusive.
func TestTheReportedSelectionUsesTheProtocolsInclusiveEnd(t *testing.T) {
	m := modelessModel("abcdef\nxyz\n")
	m.sel = &Selection{Anchor: at(0, 1), Head: at(0, 4)} // "bcd"
	got := m.reportedSelection()
	if got.StartCol != 1 || got.EndLine != 0 || got.EndCol != 3 || got.IsLine {
		t.Errorf("reported %+v, want columns 1 to 3 inclusive", got)
	}

	m.sel = &Selection{Anchor: at(0, 0), Head: at(1, 0)} // line 0, with its break
	got = m.reportedSelection()
	if !got.IsLine || got.StartLine != 0 || got.EndLine != 0 {
		t.Errorf("reported %+v, want line 0 as a whole-line selection", got)
	}
}

func TestTheRendererDrawsOnlySelectedCells(t *testing.T) {
	m := modelessModel("abcdef\nxyz\n")
	m.sel = &Selection{Anchor: at(0, 2), Head: at(1, 1)}
	if a, b := m.selectionCols(0, 6); a != 2 || b != 6 {
		t.Errorf("line 0 cells %d..%d, want 2..6 (through the line break)", a, b)
	}
	if a, b := m.selectionCols(1, 3); a != 0 || b != 0 {
		t.Errorf("line 1 cells %d..%d, want 0..0", a, b)
	}
	m.sel = &Selection{Anchor: at(0, 0), Head: at(1, 0)}
	if a, b := m.selectionCols(1, 3); a != -1 || b != -1 {
		t.Errorf("a line reached only at column 0 draws cells %d..%d, want none", a, b)
	}
}
