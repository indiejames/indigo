package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// TestInsertPastedTextPreservesNestingRelativeToFirstLine is a regression
// test for a reported bug: yanking a block whose first and last lines sit
// at the block's own baseline but whose second line is nested one level
// deeper (e.g. a wrapped import), then appending it after existing content
// on a destination line (Shift+A, then paste -- no leading Enter) lost that
// relative nesting. The bug was in blockBaseIndent: it used the *first*
// non-blank line of the pasted continuation as the baseline, but that
// first continuation line (here "\tFoo,") was itself nested deeper than
// the block's true, shallower reference line ("} from 'foo'", later in the
// same block) -- so the deeper line got mistaken for the baseline and its
// extra nesting was flattened away. Fixed by having blockBaseIndent scan
// every line and pick the least-indented one, not just the first.
func TestInsertPastedTextPreservesNestingRelativeToFirstLine(t *testing.T) {
	m := newAutoPairTestModel("func foo() {\n\tif x {\n\t\tconsole()\n\t}\n}\n")
	m.cursor = document.Pos{Line: 2, Col: len("\t\tconsole()")} // end of line, like Shift+A

	m2, _ := m.handleInsert(fakeKey("import {\n\tFoo,\n} from 'foo'"))
	got := m2.(Model)

	if got.buf.Line(2) != "\t\tconsole()import {" {
		t.Errorf("line 2 = %q, want %q (first pasted line glued onto the destination line, untouched)", got.buf.Line(2), "\t\tconsole()import {")
	}
	if got.buf.Line(3) != "\t\t\tFoo," {
		t.Errorf("line 3 = %q, want %q (one level deeper than the destination's indent, preserving relative nesting)", got.buf.Line(3), "\t\t\tFoo,")
	}
	if got.buf.Line(4) != "\t\t} from 'foo'" {
		t.Errorf("line 4 = %q, want %q (back to the destination's own indent, matching the block's baseline)", got.buf.Line(4), "\t\t} from 'foo'")
	}
}

// TestInsertPastedTextUniformBodyStaysAtDestinationLevel is a regression
// test for a bug introduced by an earlier, incomplete fix for
// TestInsertPastedTextPreservesNestingRelativeToFirstLine above: that fix
// widened the baseline computation to include the pasted text's first line
// (lines[0], glued onto the destination unreindented), which broke the
// opposite shape of block -- one whose first line genuinely is the
// shallowest one, followed by uniformly-indented body lines with no closer
// in the selection (e.g. yanking three consecutive lines from partway
// through a function). Both continuation lines shared one indentation
// level in the source and must land on exactly the destination's level, not
// one level deeper.
func TestInsertPastedTextUniformBodyStaysAtDestinationLevel(t *testing.T) {
	m := newAutoPairTestModel("func foo() {\n\tif x {\n\t\tconsole()\n\t}\n}\n")
	m.cursor = document.Pos{Line: 2, Col: len("\t\tconsole()")} // end of line, like Shift+A

	m2, _ := m.handleInsert(fakeKey("function foo() {\n\tbar();\n\tbaz();"))
	got := m2.(Model)

	if got.buf.Line(3) != "\t\tbar();" {
		t.Errorf("line 3 = %q, want %q (matches destination indent, not one level deeper)", got.buf.Line(3), "\t\tbar();")
	}
	if got.buf.Line(4) != "\t\tbaz();" {
		t.Errorf("line 4 = %q, want %q", got.buf.Line(4), "\t\tbaz();")
	}
}

// TestInsertPastedTextDedentsBeforeCloser is a regression test: pasting
// right before an already-typed closer on the same line should dedent the
// pasted continuation to match the enclosing block, the same as handleEnter
// does for a freshly typed line (TestEnterMatchesEnclosingBlockIndentBeforeCloser)
// -- insertPastedText's target indent goes through the same contextIndent
// (and therefore the same dedentTarget) call handleEnter uses.
func TestInsertPastedTextDedentsBeforeCloser(t *testing.T) {
	m := newAutoPairGoTestModel(t, "func foo() {\n\tif x {\n\t\tbar()}\n}\n")
	m.cursor = document.Pos{Line: 2, Col: len([]rune("\t\tbar()"))} // right before '}'

	m2, _ := m.handleInsert(fakeKey("\nbaz()"))
	got := m2.(Model)

	if got.buf.Line(2) != "\t\tbar()" {
		t.Errorf("line 2 = %q, want %q", got.buf.Line(2), "\t\tbar()")
	}
	if got.buf.Line(3) != "\tbaz()}" {
		t.Errorf("line 3 = %q, want %q (dedented to match \"if x {\", not one level deeper)", got.buf.Line(3), "\tbaz()}")
	}
}

// TestInsertPastedTextAtStartOfFile is a regression test: pasting into a
// brand-new empty buffer (no preceding content at all) must not panic and
// should leave pasted lines at column 0 since there's no context to inherit
// indentation from.
func TestInsertPastedTextAtStartOfFile(t *testing.T) {
	m := newAutoPairTestModel("")
	m.cursor = document.Pos{Line: 0, Col: 0}

	m2, _ := m.handleInsert(fakeKey("foo\nbar\n"))
	got := m2.(Model)

	if got.buf.Line(0) != "foo" || got.buf.Line(1) != "bar" {
		t.Errorf("Line(0)=%q Line(1)=%q, want foo/bar", got.buf.Line(0), got.buf.Line(1))
	}
}

// TestInsertPastedTextConvertsIndentStyle is a regression test: pasted
// content that uses a different indent style (tabs) than the destination
// buffer (4-space) should have its continuation lines converted, not just
// shifted by a flat prefix. reindentLines only strips the *baseline* (the
// pasted block's own leading whitespace) and replaces it with the
// destination's indent unit; extra nesting beyond the baseline is kept
// verbatim rather than recursively converted (matching TestReindentLinesTabToSpace),
// so a doubly-nested pasted line ends up mixing the new unit with the old
// one for its second level -- worth knowing since it looks odd on screen
// even though each line's *relative* depth is preserved.
func TestInsertPastedTextConvertsIndentStyle(t *testing.T) {
	m := newAutoPairTestModel("if x {\n}\n")
	m.cursor = document.Pos{Line: 0, Col: len("if x {")}
	m.detectedIndent = &config.IndentSettings{Style: "spaces", Width: 4}

	pasted := "\n\tfoo\n\t\tbar\n\tbaz"
	m2, _ := m.handleInsert(fakeKey(pasted))
	got := m2.(Model)

	if got.buf.Line(1) != "    foo" {
		t.Errorf("line 1 = %q, want %q (tab baseline converted to 4-space unit)", got.buf.Line(1), "    foo")
	}
	if got.buf.Line(2) != "    \tbar" {
		t.Errorf("line 2 = %q, want %q (baseline replaced, extra tab beyond it preserved)", got.buf.Line(2), "    \tbar")
	}
	if got.buf.Line(3) != "    baz" {
		t.Errorf("line 3 = %q, want %q", got.buf.Line(3), "    baz")
	}
}

// TestInsertPastedTextIntoDeeplyNestedContext verifies a flush-left
// multi-line paste is shifted to match a deeply nested destination while
// preserving the pasted block's own internal structure.
func TestInsertPastedTextIntoDeeplyNestedContext(t *testing.T) {
	m := newAutoPairTestModel("if a {\n\tif b {\n\t\tif c {\n\t\t}\n\t}\n}") // no trailing \n: avoid the phantom-final-line gotcha
	m.cursor = document.Pos{Line: 2, Col: len("\t\tif c {")}

	m2, _ := m.handleInsert(fakeKey("\nx := 1\nif y {\n\tz()\n}"))
	got := m2.(Model)

	want := []string{
		"if a {",
		"\tif b {",
		"\t\tif c {",
		"\t\t\tx := 1",
		"\t\t\tif y {",
		"\t\t\t\tz()",
		"\t\t\t}",
		"\t\t}",
		"\t}",
		"}",
	}
	if got.buf.LineCount() != len(want) {
		t.Fatalf("LineCount() = %d, want %d", got.buf.LineCount(), len(want))
	}
	for i, w := range want {
		if line := got.buf.Line(i); line != w {
			t.Errorf("Line(%d) = %q, want %q", i, line, w)
		}
	}
}

// TestDetectedIndentChangesAfterFormat documents a real source of the
// "indentation feels inconsistent mid-session" behavior: detectedIndent is
// re-sniffed from scratch every time a formatResultMsg lands (see
// model.go's formatResultMsg handler), so a save/format that goes through
// an external formatter or LSP formatter with a different width than what
// indigo had been assuming silently changes the indent unit used by every
// subsequent Enter, brace-expansion, and paste -- with no explicit user
// action to explain the shift. Typing "{" right after opening a file gets
// one width; typing it again after the buffer has gone through a format
// pass can silently get another.
func TestDetectedIndentChangesAfterFormat(t *testing.T) {
	m := newAutoPairTSTestModel(t, "function foo() ")
	m.cursor.Col = len([]rune("function foo() "))
	m2, _ := m.handleInsert(fakeKey("{"))
	before := m2.(Model)

	beforeUnit := before.indentUnit()

	// Simulate a formatter rewriting the buffer with a different indent
	// width than indigo's own ts default (2 spaces).
	reformatted := "function foo() {\n    x = 1;\n}\n"
	m3, _ := before.Update(formatResultMsg{content: reformatted, changed: true})
	after := m3.(Model)

	afterUnit := after.indentUnit()

	if beforeUnit == afterUnit {
		t.Fatalf("expected indentUnit to change after a format with a different width (got %q both times) -- if this now fails, the inconsistency this documents may have been fixed", beforeUnit)
	}
	if afterUnit != "    " {
		t.Errorf("indentUnit after format = %q, want %q (sniffed from the reformatted content)", afterUnit, "    ")
	}
}
