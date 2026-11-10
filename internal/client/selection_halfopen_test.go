package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// Selections are half-open: positions sit between characters, the cursor is
// the head, and an empty selection is no selection. Normal mode still draws
// a block on a character (cursorCell), which these tests check too, since it
// is what the user sees.

var (
	shiftRight = tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModShift}
	shiftLeft  = tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModShift}
	plainLeft  = tea.KeyPressMsg{Code: tea.KeyLeft}
	plainRight = tea.KeyPressMsg{Code: tea.KeyRight}
)

func at(line, col int) document.Pos { return document.Pos{Line: line, Col: col} }

// normalKeys presses each key in Normal mode, as handleNormal sees them.
func normalKeys(t *testing.T, m Model, keys ...tea.KeyMsg) Model {
	t.Helper()
	for _, k := range keys {
		next, _ := m.handleNormal(k)
		m = next.(Model)
	}
	return m
}

func modelAt(content string, cursor document.Pos) Model {
	m := newTestModel(content)
	m.cursor = cursor
	return m
}

// TestOneShiftRightSelectsOneCharacter is the change asked for: the first
// Shift+→ selects the character under the block, not it and the next one.
func TestOneShiftRightSelectsOneCharacter(t *testing.T) {
	m := normalKeys(t, newTestModel("hello\n"), shiftRight)
	if got := m.selectedText(); got != "h" {
		t.Errorf("selected %q, want %q", got, "h")
	}
	if m.cursor != at(0, 1) || m.cursorCell() != at(0, 0) {
		t.Errorf("cursor %v drawn at %v, want after the h and drawn on it", m.cursor, m.cursorCell())
	}
	m = normalKeys(t, m, shiftRight)
	if got := m.selectedText(); got != "he" || m.cursorCell() != at(0, 1) {
		t.Errorf("selected %q drawn at %v, want %q with the block on the e", got, m.cursorCell(), "he")
	}
}

func TestShiftLeftSelectsTheCharacterBeforeTheBlock(t *testing.T) {
	m := normalKeys(t, modelAt("hello\n", at(0, 3)), shiftLeft)
	if got := m.selectedText(); got != "l" || m.cursorCell() != at(0, 2) {
		t.Errorf("selected %q drawn at %v, want the first l with the block on it", got, m.cursorCell())
	}
}

func TestShiftingBackToTheAnchorSelectsNothing(t *testing.T) {
	m := normalKeys(t, newTestModel("hello\n"), shiftRight, shiftLeft)
	if m.sel != nil {
		t.Errorf("sel = %+v, want none: an empty selection is no selection", *m.sel)
	}
}

func TestShiftRightCrossesTheLineBreak(t *testing.T) {
	m := normalKeys(t, modelAt("ab\ncd\n", at(0, 2)), shiftRight)
	if got := m.selectedText(); got != "\n" {
		t.Errorf("selected %q, want the line break", got)
	}
	if m.cursor != at(1, 0) {
		t.Errorf("cursor = %+v, want the start of the next line", m.cursor)
	}
}

// TestShiftRightReachesTheLastCharacter: the Normal-mode cursor cannot rest
// past the buffer's last character, but a selection must be able to end
// there, or the last character could never be selected.
func TestShiftRightReachesTheLastCharacter(t *testing.T) {
	m := normalKeys(t, modelAt("ab", at(0, 1)), shiftRight)
	if got := m.selectedText(); got != "b" {
		t.Errorf("selected %q, want %q", got, "b")
	}
}

// TestMotionsMoveFromTheBlock: plain arrows leave a selection from the
// character the block is on, so they move one cell as they look like they do.
func TestMotionsMoveFromTheBlock(t *testing.T) {
	sel := func() Model { return normalKeys(t, modelAt("abcdef\n", at(0, 1)), shiftRight, shiftRight, shiftRight) }
	m := sel() // "bcd", block on d
	if m.cursorCell() != at(0, 3) {
		t.Fatalf("block at %v, want on the d", m.cursorCell())
	}
	if m = normalKeys(t, sel(), plainRight); m.sel != nil || m.cursor != at(0, 4) {
		t.Errorf("→: sel %v cursor %v, want no selection and the cursor on the e", m.sel, m.cursor)
	}
	if m = normalKeys(t, sel(), plainLeft); m.sel != nil || m.cursor != at(0, 2) {
		t.Errorf("←: sel %v cursor %v, want no selection and the cursor on the c", m.sel, m.cursor)
	}
}

func TestInsertAndAppendGoEitherSideOfTheBlock(t *testing.T) {
	for _, c := range []struct {
		key  string
		want document.Pos
	}{{"i", at(0, 3)}, {"a", at(0, 4)}} {
		m := normalKeys(t, modelAt("abcdef\n", at(0, 1)), shiftRight, shiftRight, shiftRight)
		m = normalKeys(t, m, fakeKey(c.key))
		if m.mode != ModeInsert || m.sel != nil || m.cursor != c.want {
			t.Errorf("%s: mode %v sel %v cursor %v, want insert at %v", c.key, m.mode, m.sel, m.cursor, c.want)
		}
	}
}

// TestDeleteWithoutASelectionTakesTheCharacterUnderTheBlock: the character
// after the cursor, which is the one the block is drawn on.
func TestDeleteWithoutASelectionTakesTheCharacterUnderTheBlock(t *testing.T) {
	m := normalKeys(t, modelAt("abc\n", at(0, 1)), fakeKey("d"))
	if got := m.buf.Content(); got != "ac\n" {
		t.Errorf("content = %q, want %q", got, "ac\n")
	}
}

func TestDeleteRemovesExactlyTheSelection(t *testing.T) {
	m := normalKeys(t, modelAt("abcdef\n", at(0, 1)), shiftRight, shiftRight, fakeKey("d"))
	if got := m.buf.Content(); got != "adef\n" || m.cursor != at(0, 1) {
		t.Errorf("content = %q cursor %v, want %q with the cursor where it was", got, m.cursor, "adef\n")
	}
}

func TestXSelectsWholeLinesAndDDeletesThem(t *testing.T) {
	m := normalKeys(t, newTestModel("one\ntwo\nthree\n"), fakeKey("x"))
	if got := m.selectedText(); got != "one\n" || !m.isLineSelection() {
		t.Fatalf("x selected %q, want the first line with its break", got)
	}
	m = normalKeys(t, m, fakeKey("x"))
	if got := m.selectedText(); got != "one\ntwo\n" {
		t.Fatalf("x x selected %q, want two lines", got)
	}
	m = normalKeys(t, m, fakeKey("d"))
	if got := m.buf.Content(); got != "three\n" {
		t.Errorf("x x d left %q, want %q", got, "three\n")
	}
}

func TestEExtendsToTheEndOfTheWord(t *testing.T) {
	m := normalKeys(t, newTestModel("alpha beta\n"), fakeKey("E"))
	if got := m.selectedText(); got != "alpha" || m.cursorCell() != at(0, 4) {
		t.Errorf("E selected %q drawn at %v, want alpha with the block on its a", got, m.cursorCell())
	}
	// On a word's last letter, E takes it and the next word, as it always has.
	m = normalKeys(t, modelAt("alpha beta\n", at(0, 4)), fakeKey("E"))
	if got := m.selectedText(); got != "a beta" {
		t.Errorf("E from alpha's last letter selected %q, want %q", got, "a beta")
	}
}

func TestSelectToMarkIncludesBothEnds(t *testing.T) {
	m := normalKeys(t, modelAt("abcdef\n", at(0, 4)), fakeKey("z"))
	m.cursor = at(0, 1)
	m = normalKeys(t, m, fakeKey("Z"))
	if got := m.selectedText(); got != "bcde" {
		t.Errorf("selected %q, want from the cursor's character to the mark's, %q", got, "bcde")
	}
}

// TestADragIncludesTheCellUnderThePointer: dragging over characters selects
// those characters, in either direction.
func TestADragIncludesTheCellUnderThePointer(t *testing.T) {
	m := newTestModel("abcdef\n")
	m.selectCells(at(0, 1), at(0, 3))
	if got := m.selectedText(); got != "bcd" || m.cursorCell() != at(0, 3) {
		t.Errorf("forward: selected %q drawn at %v, want bcd with the block on d", got, m.cursorCell())
	}
	m.selectCells(at(0, 3), at(0, 1))
	if got := m.selectedText(); got != "bcd" || m.cursorCell() != at(0, 1) {
		t.Errorf("backward: selected %q drawn at %v, want bcd with the block on b", got, m.cursorCell())
	}
}

func TestSelectAllCoversTheWholeBuffer(t *testing.T) {
	m := normalKeys(t, newTestModel("a\nb\n"), fakeKey("%"))
	if got := m.selectedText(); got != "a\nb\n" {
		t.Errorf("selected %q, want the whole buffer", got)
	}
}

// TestASelectionEndingAtColumnZeroLeavesThatLineOut: a selection down to the
// start of line 2 covers lines 0 and 1 for line-wise commands, as in GUI
// editors.
func TestASelectionEndingAtColumnZeroLeavesThatLineOut(t *testing.T) {
	m := newTestModel("a\nb\nc\n")
	m.sel = &Selection{Anchor: at(0, 0), Head: at(2, 0)}
	m.cursor = m.sel.Head
	if first, last := m.selectedLines(); first != 0 || last != 1 {
		t.Errorf("lines %d..%d, want 0..1", first, last)
	}
}

func TestInsideBracketsSelectsTheContents(t *testing.T) {
	m := modelAt("f(abc)\n", at(0, 3))
	out, _ := executeSelectInsideBrackets(m)
	inside := out.(Model)
	if got := inside.selectedText(); got != "abc" {
		t.Errorf("selected %q, want %q", got, "abc")
	}
	m = modelAt("f()\n", at(0, 2))
	out, _ = executeSelectInsideBrackets(m)
	if got := out.(Model); got.sel != nil || got.cursor != at(0, 2) {
		t.Errorf("empty brackets: sel = %v cursor = %+v, want no selection, cursor between them", got.sel, got.cursor)
	}
}

func TestCtrlDSelectsTheWordAndThenTheNextOccurrence(t *testing.T) {
	m := normalKeys(t, modelAt("foo bar foo\n", at(0, 1)), tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if got := m.selectedText(); got != "foo" {
		t.Fatalf("selected %q, want %q", got, "foo")
	}
	m = normalKeys(t, m, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if len(m.extraCursors) != 1 || m.textForSelection(m.extraCursors[0].sel) != "foo" {
		t.Fatalf("extra cursors = %+v, want one on the second foo", m.extraCursors)
	}
	if got := m.cellOf(m.extraCursors[0].pos, m.extraCursors[0].sel); got != at(0, 10) {
		t.Errorf("second cursor drawn at %+v, want on the second foo's last letter", got)
	}
}

// TestLSPRequestsAskAboutTheBlocksCharacter: after a word is selected the
// cursor is past it, so a hover or go-to-definition sent from the cursor
// would ask about the next character instead of the word.
func TestLSPRequestsAskAboutTheBlocksCharacter(t *testing.T) {
	m := newTestModel("foo(bar)\n")
	m.sel = &Selection{Anchor: at(0, 0), Head: at(0, 3)}
	m.cursor = m.sel.Head
	if got := m.cursorCell(); got != at(0, 2) {
		t.Errorf("requests would ask about %v, want the last o of foo", got)
	}
}

// TestTheReportedSelectionUsesTheProtocolsInclusiveEnd: agents read the
// selection through get_active_context, whose protocol end is inclusive.
func TestTheReportedSelectionUsesTheProtocolsInclusiveEnd(t *testing.T) {
	m := newTestModel("abcdef\nxyz\n")
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
	m := newTestModel("abcdef\nxyz\n")
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
