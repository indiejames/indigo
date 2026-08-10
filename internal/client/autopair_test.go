package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

func newAutoPairTSTestModel(t *testing.T, content string) Model {
	t.Helper()
	m := newAutoPairTestModel(content)
	m.buf = document.New("test.ts", content)
	m.hlr = highlight.New("test.ts")
	if m.hlr == nil {
		t.Skip("no TypeScript highlighter registered; run with -tags lang_all (or lang_typescript)")
	}
	return m
}

func newAutoPairTestModel(content string) Model {
	m := newTestModel(content)
	m.mode = ModeInsert
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial
	return m
}

func TestAutoPairInsertsClosingBracket(t *testing.T) {
	m := newAutoPairTestModel("\n")
	m2, _ := m.handleInsert(fakeKey("("))
	got := m2.(Model)

	if got.buf.Line(0) != "()" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "()")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1 (between the pair)", got.cursor.Col)
	}
}

func TestAutoPairInsertsClosingQuote(t *testing.T) {
	m := newAutoPairTestModel("\n")
	m2, _ := m.handleInsert(fakeKey("\""))
	got := m2.(Model)

	if got.buf.Line(0) != "\"\"" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), "\"\"")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1 (between the pair)", got.cursor.Col)
	}
}

func TestAutoPairSkipsQuoteInsideWord(t *testing.T) {
	m := newAutoPairTestModel("dont\n")
	m.cursor.Col = 3 // between "don" and "t"
	m2, _ := m.handleInsert(fakeKey("'"))
	got := m2.(Model)

	if got.buf.Line(0) != "don't" {
		t.Errorf("line = %q, want %q (no pairing mid-word)", got.buf.Line(0), "don't")
	}
	if got.cursor.Col != 4 {
		t.Errorf("cursor.Col = %d, want 4", got.cursor.Col)
	}
}

func TestAutoPairTypingCloserSkipsOverExistingOne(t *testing.T) {
	m := newAutoPairTestModel("()\n")
	m.cursor.Col = 1 // between ( and )
	m2, _ := m.handleInsert(fakeKey(")"))
	got := m2.(Model)

	if got.buf.Line(0) != "()" {
		t.Errorf("line = %q, want %q (no duplicate closer)", got.buf.Line(0), "()")
	}
	if got.cursor.Col != 2 {
		t.Errorf("cursor.Col = %d, want 2 (moved past the closer)", got.cursor.Col)
	}
}

func TestAutoPairTypingCloserWithoutMatchInsertsNormally(t *testing.T) {
	m := newAutoPairTestModel("\n")
	m2, _ := m.handleInsert(fakeKey(")"))
	got := m2.(Model)

	if got.buf.Line(0) != ")" {
		t.Errorf("line = %q, want %q", got.buf.Line(0), ")")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1", got.cursor.Col)
	}
}

func TestAutoPairOpenerBeforeStrayQuotePairsWithIt(t *testing.T) {
	// Simulates: type '"' (-> "\"\""), forward-delete the opening quote
	// (leaving a lone, unmatched '"'), then type '"' again — should pair
	// with the stray quote instead of skipping over it and leaving just
	// one '"' behind.
	m := newAutoPairTestModel("\"\n")
	m.cursor.Col = 0 // before the stray, unmatched '"'
	m2, _ := m.handleInsert(fakeKey("\""))
	got := m2.(Model)

	if got.buf.Line(0) != "\"\"" {
		t.Errorf("line = %q, want %q (opener should pair with the stray quote)", got.buf.Line(0), "\"\"")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1 (between the pair)", got.cursor.Col)
	}
}

func TestAutoPairQuoteInsideOpenStringStillSkipsOver(t *testing.T) {
	m := newAutoPairTestModel("\"hello\"\n")
	m.cursor.Col = 6 // between "o" and the closing quote, inside the string
	m2, _ := m.handleInsert(fakeKey("\""))
	got := m2.(Model)

	if got.buf.Line(0) != "\"hello\"" {
		t.Errorf("line = %q, want %q (no duplicate quote)", got.buf.Line(0), "\"hello\"")
	}
	if got.cursor.Col != 7 {
		t.Errorf("cursor.Col = %d, want 7 (moved past the closing quote)", got.cursor.Col)
	}
}

func TestAutoPairOpenerBeforeUnmatchedCloserDoesNotNest(t *testing.T) {
	m := newAutoPairTestModel(")\n")
	m.cursor.Col = 0 // before the stray, unmatched ')'
	m2, _ := m.handleInsert(fakeKey("("))
	got := m2.(Model)

	if got.buf.Line(0) != "()" {
		t.Errorf("line = %q, want %q (opener should complete the existing closer)", got.buf.Line(0), "()")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1 (between the pair)", got.cursor.Col)
	}
}

func TestAutoPairOpenerBeforeMatchedCloserStillNests(t *testing.T) {
	m := newAutoPairTestModel("(a)\n")
	m.cursor.Col = 2 // between "a" and ")", which already closes the leading "("
	m2, _ := m.handleInsert(fakeKey("("))
	got := m2.(Model)

	if got.buf.Line(0) != "(a())" {
		t.Errorf("line = %q, want %q (closer belongs to the earlier opener, so this one still nests)", got.buf.Line(0), "(a())")
	}
	if got.cursor.Col != 3 {
		t.Errorf("cursor.Col = %d, want 3 (between the new pair)", got.cursor.Col)
	}
}

func TestAutoPairBackspaceCollapsesEmptyPair(t *testing.T) {
	m := newAutoPairTestModel("()\n")
	m.cursor.Col = 1 // between ( and )
	m2, _ := m.handleInsert(fakeKey("backspace"))
	got := m2.(Model)

	if got.buf.Line(0) != "" {
		t.Errorf("line = %q, want %q (both chars deleted)", got.buf.Line(0), "")
	}
	if got.cursor.Col != 0 {
		t.Errorf("cursor.Col = %d, want 0", got.cursor.Col)
	}
}

func TestAutoPairBackspaceWithoutPairDeletesOneChar(t *testing.T) {
	m := newAutoPairTestModel("(x)\n")
	m.cursor.Col = 2 // between x and )
	m2, _ := m.handleInsert(fakeKey("backspace"))
	got := m2.(Model)

	if got.buf.Line(0) != "()" {
		t.Errorf("line = %q, want %q (only the char before cursor deleted)", got.buf.Line(0), "()")
	}
	if got.cursor.Col != 1 {
		t.Errorf("cursor.Col = %d, want 1", got.cursor.Col)
	}
}

func TestAutoPairBraceExpandsToBlock(t *testing.T) {
	m := newAutoPairTestModel("")
	m2, _ := m.handleInsert(fakeKey("{"))
	got := m2.(Model)

	if got.buf.LineCount() != 3 {
		t.Fatalf("LineCount() = %d, want 3", got.buf.LineCount())
	}
	if got.buf.Line(0) != "{" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "{")
	}
	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q", got.buf.Line(1), "\t")
	}
	if got.buf.Line(2) != "}" {
		t.Errorf("line 2 = %q, want %q", got.buf.Line(2), "}")
	}
	if got.cursor.Line != 1 || got.cursor.Col != 1 {
		t.Errorf("cursor = (%d,%d), want (1,1) (end of the indented empty line)", got.cursor.Line, got.cursor.Col)
	}
}

func TestAutoPairBraceExpandsToBlockPreservingIndent(t *testing.T) {
	m := newAutoPairTestModel("\tfunc foo() \n")
	m.cursor.Col = len([]rune("\tfunc foo() "))
	m2, _ := m.handleInsert(fakeKey("{"))
	got := m2.(Model)

	if got.buf.Line(0) != "\tfunc foo() {" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "\tfunc foo() {")
	}
	if got.buf.Line(1) != "\t\t" {
		t.Errorf("line 1 = %q, want %q (base indent + one level)", got.buf.Line(1), "\t\t")
	}
	if got.buf.Line(2) != "\t}" {
		t.Errorf("line 2 = %q, want %q (closing brace matches opening line's indent)", got.buf.Line(2), "\t}")
	}
	if got.cursor.Line != 1 || got.cursor.Col != 2 {
		t.Errorf("cursor = (%d,%d), want (1,2)", got.cursor.Line, got.cursor.Col)
	}
}

func TestAutoPairBraceExpandsToBlockForFunctionBody(t *testing.T) {
	m := newAutoPairTSTestModel(t, "function foo() ")
	m.cursor.Col = len([]rune("function foo() "))
	m2, _ := m.handleInsert(fakeKey("{"))
	got := m2.(Model)

	if got.buf.LineCount() != 3 {
		t.Fatalf("LineCount() = %d, want 3 (function body should expand)", got.buf.LineCount())
	}
	if got.buf.Line(0) != "function foo() {" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "function foo() {")
	}
	if got.buf.Line(2) != "}" {
		t.Errorf("line 2 = %q, want %q", got.buf.Line(2), "}")
	}
}

func TestAutoPairBraceDoesNotExpandInsideString(t *testing.T) {
	m := newAutoPairTSTestModel(t, `const s = "hello "`)
	m.cursor.Col = len([]rune(`const s = "hello `)) // inside the string, right after "hello "
	m2, _ := m.handleInsert(fakeKey("{"))
	got := m2.(Model)

	if got.buf.LineCount() != 1 {
		t.Fatalf("LineCount() = %d, want 1 (typing '{' inside a string shouldn't split it across lines)", got.buf.LineCount())
	}
	want := `const s = "hello {}"`
	if got.buf.Line(0) != want {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), want)
	}
	if got.cursor.Col != len([]rune(`const s = "hello {`)) {
		t.Errorf("cursor.Col = %d, want %d (between the pair)", got.cursor.Col, len([]rune(`const s = "hello {`)))
	}
}

func TestAutoPairBraceDoesNotExpandForImportList(t *testing.T) {
	m := newAutoPairTSTestModel(t, "import  from 'foo'")
	m.cursor.Col = len([]rune("import ")) // right after "import "
	m2, _ := m.handleInsert(fakeKey("{"))
	got := m2.(Model)

	if got.buf.LineCount() != 1 {
		t.Fatalf("LineCount() = %d, want 1 (import list shouldn't expand)", got.buf.LineCount())
	}
	want := "import {} from 'foo'"
	if got.buf.Line(0) != want {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), want)
	}
	if got.cursor.Col != len([]rune("import {")) {
		t.Errorf("cursor.Col = %d, want %d (between the pair)", got.cursor.Col, len([]rune("import {")))
	}
}
