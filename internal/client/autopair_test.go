package client

import "testing"

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
