package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

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
