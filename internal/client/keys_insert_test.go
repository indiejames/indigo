package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// TestExecuteInsertTabTabsStyleInsertsLiteralTab is a regression test:
// "tabs" style keeps the original unconditional behavior — a tab is its own
// stop, so there's no tabstop math to do regardless of cursor column.
func TestExecuteInsertTabTabsStyleInsertsLiteralTab(t *testing.T) {
	m := newAutoPairTestModel("ab\n")
	m.cfg = &config.Config{IndentStyle: "tabs", IndentWidth: 4}
	m.cursor.Col = 2

	m2, _ := m.handleInsert(fakeKey("tab"))
	got := m2.(Model)

	if got.buf.Line(0) != "ab\t" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "ab\t")
	}
	if got.cursor.Col != 3 {
		t.Errorf("cursor.Col = %d, want 3", got.cursor.Col)
	}
}

// TestExecuteInsertTabSpacesStyleAtLineStartUsesFullWidth verifies that at a
// stop-aligned column (line start), spaces style still inserts a full
// indentUnit()-width of spaces — same as before this change.
func TestExecuteInsertTabSpacesStyleAtLineStartUsesFullWidth(t *testing.T) {
	m := newAutoPairTestModel("\n")
	m.cfg = &config.Config{IndentStyle: "spaces", IndentWidth: 4}

	m2, _ := m.handleInsert(fakeKey("tab"))
	got := m2.(Model)

	if got.buf.Line(0) != "    " {
		t.Errorf("line = %q, want 4 spaces", got.buf.Line(0))
	}
	if got.cursor.Col != 4 {
		t.Errorf("cursor.Col = %d, want 4", got.cursor.Col)
	}
}

// TestExecuteInsertTabSpacesStyleMidLinePadsToNextStop is the core
// regression this change is about: pressing Tab mid-line in spaces mode
// should pad only to the next tab stop (VS Code-style), not insert a fixed
// indentUnit()-width every time.
func TestExecuteInsertTabSpacesStyleMidLinePadsToNextStop(t *testing.T) {
	m := newAutoPairTestModel("ab\n")
	m.cfg = &config.Config{IndentStyle: "spaces", IndentWidth: 4}
	m.cursor.Col = 2 // visual column 2, next stop at 4 -> 2 spaces

	m2, _ := m.handleInsert(fakeKey("tab"))
	got := m2.(Model)

	if got.buf.Line(0) != "ab  " {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "ab  ")
	}
	if got.cursor.Col != 4 {
		t.Errorf("cursor.Col = %d, want 4", got.cursor.Col)
	}
}

// TestExecuteInsertTabSpacesStyleAccountsForExistingTabs verifies the
// tabstop math uses on-screen visual column, not raw rune column, so a tab
// already on the line before the cursor is accounted for correctly.
func TestExecuteInsertTabSpacesStyleAccountsForExistingTabs(t *testing.T) {
	m := newAutoPairTestModel("\ta\n") // "\t" expands to visual col 4, "a" -> col 5
	m.cfg = &config.Config{IndentStyle: "spaces", IndentWidth: 4}
	m.cursor.Col = 2 // rune col 2 (after "\ta"), visual col 5 -> next stop at 8 -> 3 spaces

	m2, _ := m.handleInsert(fakeKey("tab"))
	got := m2.(Model)

	if got.buf.Line(0) != "\ta   " {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "\ta   ")
	}
}

// TestExecuteInsertTabSpacesStyleMulticursorPadsIndependently verifies each
// cursor gets its own tabstop-relative pad amount rather than all cursors
// receiving identical text, since applyInsertToAllCursors previously applied
// one fixed string everywhere.
func TestExecuteInsertTabSpacesStyleMulticursorPadsIndependently(t *testing.T) {
	m := newAutoPairTestModel("a\nbb\n")
	m.cfg = &config.Config{IndentStyle: "spaces", IndentWidth: 4}
	m.cursor = document.Pos{Line: 0, Col: 1}                                          // visual col 1 -> 3 spaces
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 2}, goalCol: -1}} // visual col 2 -> 2 spaces

	m2, _ := m.handleInsert(fakeKey("tab"))
	got := m2.(Model)

	if got.buf.Line(0) != "a   " {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "a   ")
	}
	if got.buf.Line(1) != "bb  " {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "bb  ")
	}
}

// TestInsertPastedTextIndentsAfterOpenBrace is a regression test: a
// multi-line paste landing right after an opener should have its
// continuation lines indented one level, the same rule handleEnter uses
// for a freshly typed line there. The first pasted line is left untouched
// since it continues whatever's already before the cursor.
func TestInsertPastedTextIndentsAfterOpenBrace(t *testing.T) {
	m := newAutoPairTestModel("if x {\n}\n")
	m.cursor = document.Pos{Line: 0, Col: len("if x {")}

	m2, _ := m.handleInsert(fakeKey("a\nb"))
	got := m2.(Model)

	if line := got.buf.Line(0); line != "if x {a" {
		t.Errorf("Line(0) = %q, want %q", line, "if x {a")
	}
	if line := got.buf.Line(1); line != "\tb" {
		t.Errorf("Line(1) = %q, want %q (indented one level after the open brace)", line, "\tb")
	}
	if line := got.buf.Line(2); line != "}" {
		t.Errorf("Line(2) = %q, want %q", line, "}")
	}
	if got.cursor != (document.Pos{Line: 1, Col: 2}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:2} (end of last pasted line)", got.cursor)
	}
}

// TestInsertPastedTextPreservesRelativeIndent is a regression test:
// reindenting a multi-line paste shifts every continuation line by the
// same amount rather than flattening it, so the pasted content's own
// internal nesting survives landing at a different indent level.
func TestInsertPastedTextPreservesRelativeIndent(t *testing.T) {
	m := newAutoPairTestModel("func foo() {\n}\n")
	m.cursor = document.Pos{Line: 0, Col: len("func foo() {")}

	m2, _ := m.handleInsert(fakeKey("x := 1\nif x {\n\tbar()\n}"))
	got := m2.(Model)

	want := []string{
		"func foo() {x := 1",
		"\tif x {",
		"\t\tbar()",
		"\t}",
		"}",
	}
	for i, w := range want {
		if line := got.buf.Line(i); line != w {
			t.Errorf("Line(%d) = %q, want %q", i, line, w)
		}
	}
}

// TestInsertPastedTextPlacesCursorAtEndOfLastLine is a regression test for
// a pre-existing bug: the cursor after a multi-line insert was computed as
// the old column plus the total rune count (which counts embedded
// newlines as columns), landing it off the end of the buffer instead of at
// the end of the last pasted line.
func TestInsertPastedTextPlacesCursorAtEndOfLastLine(t *testing.T) {
	m := newAutoPairTestModel("foo\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handleInsert(fakeKey("bar\nbaz"))
	got := m2.(Model)

	if got.buf.Line(0) != "foobar" || got.buf.Line(1) != "baz" {
		t.Errorf("Line(0)=%q Line(1)=%q, want foobar/baz", got.buf.Line(0), got.buf.Line(1))
	}
	if got.cursor != (document.Pos{Line: 1, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:3} (end of last pasted line)", got.cursor)
	}
}

// TestInsertPastedTextDoesNotReindentSingleLine is a regression test: a
// single-line "paste" (or multi-rune IME commit) has no continuation lines
// to reindent, so it must go through untouched rather than being treated
// as a zero-line block.
func TestInsertPastedTextDoesNotReindentSingleLine(t *testing.T) {
	m := newAutoPairTestModel("if x {\n}\n")
	m.cursor = document.Pos{Line: 0, Col: len("if x {")}

	m2, _ := m.handleInsert(fakeKey("abc"))
	got := m2.(Model)

	if line := got.buf.Line(0); line != "if x {abc" {
		t.Errorf("Line(0) = %q, want %q", line, "if x {abc")
	}
	if got.buf.LineCount() != 3 {
		t.Errorf("LineCount() = %d, want 3 (no line was added)", got.buf.LineCount())
	}
}
