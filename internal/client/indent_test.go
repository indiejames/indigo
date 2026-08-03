package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

func newAutoPairGoTestModel(t *testing.T, content string) Model {
	t.Helper()
	m := newAutoPairTestModel(content)
	m.buf = document.New("test.go", content)
	m.hlr = highlight.New("test.go")
	if m.hlr == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}
	return m
}

func TestEnterCopiesCurrentLineIndent(t *testing.T) {
	m := newAutoPairTestModel("\tfoo\n")
	m.cursor.Col = len([]rune("\tfoo"))
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q (same indent as line 0)", got.buf.Line(1), "\t")
	}
	if got.cursor.Line != 1 || got.cursor.Col != 1 {
		t.Errorf("cursor = (%d,%d), want (1,1)", got.cursor.Line, got.cursor.Col)
	}
}

func TestEnterIndentsAfterTrailingOpenBracket(t *testing.T) {
	m := newAutoPairTestModel("\tif x [\n")
	m.cursor.Col = len([]rune("\tif x ["))
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(1) != "\t\t" {
		t.Errorf("line 1 = %q, want %q (base indent + one level)", got.buf.Line(1), "\t\t")
	}
}

func TestEnterIndentsAfterTrailingColon(t *testing.T) {
	m := newAutoPairTestModel("def foo():\n")
	m.cursor.Col = len([]rune("def foo():"))
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "\t")
	}
}

func TestEnterDoesNotIndentPlainLine(t *testing.T) {
	m := newAutoPairTestModel("foo\n")
	m.cursor.Col = len([]rune("foo"))
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(1) != "" {
		t.Errorf("line 1 = %q, want empty (no trailing opener)", got.buf.Line(1))
	}
}

func TestEnterSplitsEmptyParenPair(t *testing.T) {
	m := newAutoPairTestModel("\tfoo()\n")
	m.cursor.Col = len([]rune("\tfoo(")) // between ( and )
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.LineCount() != 4 { // "\tfoo(", "\t\t", "\t)", "" (trailing)
		t.Fatalf("LineCount() = %d, want 4", got.buf.LineCount())
	}
	if got.buf.Line(0) != "\tfoo(" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "\tfoo(")
	}
	if got.buf.Line(1) != "\t\t" {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "\t\t")
	}
	if got.buf.Line(2) != "\t)" {
		t.Errorf("line 2 = %q, want %q", got.buf.Line(2), "\t)")
	}
	if got.cursor.Line != 1 || got.cursor.Col != 2 {
		t.Errorf("cursor = (%d,%d), want (1,2)", got.cursor.Line, got.cursor.Col)
	}
}

func TestEnterMidLineKeepsTrailingTextOnClosingLine(t *testing.T) {
	m := newAutoPairTestModel("\tfoobar\n")
	m.cursor.Col = len([]rune("\tfoo")) // between "foo" and "bar"
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(0) != "\tfoo" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "\tfoo")
	}
	if got.buf.Line(1) != "\tbar" {
		t.Errorf("line 1 = %q, want %q (indent copied, trailing text preserved)", got.buf.Line(1), "\tbar")
	}
}

func TestEnterUsesConfiguredSpacesIndent(t *testing.T) {
	m := newAutoPairTestModel("if x:\n")
	m.cursor.Col = len([]rune("if x:"))
	m.cfg = &config.Config{IndentStyle: "spaces", IndentWidth: 2}
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(1) != "  " {
		t.Errorf("line 1 = %q, want %q (2-space indent from config)", got.buf.Line(1), "  ")
	}
}

func TestEnterMatchesEnclosingBlockIndentBeforeCloser(t *testing.T) {
	// "if x {" opens at one tab. "bar()}" on the next line packs the body
	// and closer onto one line (e.g. pasted or typed without autopair).
	// Pressing Enter right before the '}' should push it onto its own line
	// dedented to match "if x {", not "bar()"'s deeper indent — Phase 1's
	// heuristic alone can't do this, since the last char before the cursor
	// is ')', which doesn't signal an indent change either way.
	m := newAutoPairGoTestModel(t, "func foo() {\n\tif x {\n\t\tbar()}\n}\n")
	m.cursor = document.Pos{Line: 2, Col: len([]rune("\t\tbar()"))} // right before '}'
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(2) != "\t\tbar()" {
		t.Errorf("line 2 = %q, want %q", got.buf.Line(2), "\t\tbar()")
	}
	if got.buf.Line(3) != "\t}" {
		t.Errorf("line 3 = %q, want %q (dedented to match \"if x {\")", got.buf.Line(3), "\t}")
	}
	if got.buf.Line(4) != "}" {
		t.Errorf("line 4 = %q, want %q", got.buf.Line(4), "}")
	}
	if got.cursor.Line != 3 || got.cursor.Col != 1 {
		t.Errorf("cursor = (%d,%d), want (3,1)", got.cursor.Line, got.cursor.Col)
	}
}

func TestEnterDetectedIndentOverridesConfig(t *testing.T) {
	// Buffer content already uses 4-space indentation; config says tabs.
	// The buffer's own style should win so edits stay consistent with it.
	m := newAutoPairTestModel("def foo():\n    pass\n")
	m.cfg = &config.Config{IndentStyle: "tabs"}
	m.detectedIndent = detectIndentSettings(m.buf.Content())
	m.cursor = document.Pos{Line: 1, Col: len([]rune("    pass"))}
	m2, _ := m.handleInsert(fakeKey("enter"))
	got := m2.(Model)

	if got.buf.Line(2) != "    " {
		t.Errorf("line 2 = %q, want %q (detected 4-space indent, not config tabs)", got.buf.Line(2), "    ")
	}
}
