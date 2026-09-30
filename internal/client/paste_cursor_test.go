package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// A single-line bracketed paste must leave the cursor at the end of what was
// pasted, not at a column measured from the start of the line.
//
// Reported from a real session: insert mode at the end of a line, cmd+v, and
// the cursor jumped backwards to somewhere unrelated in the line. The column
// was being set to the pasted text's own length, which is only correct when
// the paste added lines — and handlePaste is the one caller that passes
// single-line text, so this path was never exercised by the tests written for
// the multi-line case.
func TestSingleLinePasteLeavesCursorAfterPastedText(t *testing.T) {
	const line = "const somethingReasonablyLong = compute();"
	m := newAutoPairTestModel(line + "\n")
	m.cursor = document.Pos{Line: 0, Col: len([]rune(line))} // end of line, as after Shift+A

	m2, _ := m.handlePaste(" // note")
	got := m2.(Model)

	if want := line + " // note"; got.buf.Line(0) != want {
		t.Errorf("line = %q, want %q", got.buf.Line(0), want)
	}
	if want := len([]rune(line)) + len(" // note"); got.cursor.Col != want {
		t.Errorf("cursor.Col = %d, want %d (end of the pasted text, not %d — the paste's own length)",
			got.cursor.Col, want, len(" // note"))
	}
	if got.cursor.Line != 0 {
		t.Errorf("cursor.Line = %d, want 0: a paste with no newline must not move the cursor off its line", got.cursor.Line)
	}
}

// The same, pasting into the middle of a line rather than at its end.
func TestSingleLinePasteMidLineLeavesCursorAfterPastedText(t *testing.T) {
	m := newAutoPairTestModel("abcdef\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handlePaste("XY")
	got := m2.(Model)

	if got.buf.Line(0) != "abcXYdef" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "abcXYdef")
	}
	if got.cursor.Col != 5 {
		t.Errorf("cursor.Col = %d, want 5 (just after the pasted \"XY\")", got.cursor.Col)
	}
}

// Multi-line paste is the case the column arithmetic was originally written
// for: the cursor ends on a line whose whole content is pasted text, so the
// column is that line's own length — and must not have the old column added
// to it. Kept alongside the fix because the two branches are one expression.
func TestMultiLinePasteLeavesCursorAtEndOfLastPastedLine(t *testing.T) {
	m := newAutoPairTestModel("abcdef\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handlePaste("X\nYZ")
	got := m2.(Model)

	if got.buf.Line(0) != "abcX" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "abcX")
	}
	if got.buf.Line(1) != "YZdef" {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "YZdef")
	}
	if got.cursor.Line != 1 || got.cursor.Col != 2 {
		t.Errorf("cursor = %d:%d, want 1:2 (after \"YZ\" on the new last line)", got.cursor.Line, got.cursor.Col)
	}
}
