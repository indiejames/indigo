package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// newCompletionApplyTestModel builds an insert-mode model with a zero-value RPC
// (ClientID() just reads a field; the returned send Cmds are never executed, so
// no dial happens).
func newCompletionApplyTestModel(content string) Model {
	m := newTestModel(content)
	m.mode = ModeInsert
	m.rpc = &RPC{}
	return m
}

// TestApplyCompletionInsertsAutoImportAndShiftsCursor is a regression test for
// the auto-import apply path: accepting a completion whose resolved item carries
// an additionalTextEdit inserting an import line at the top of the file must both
// replace the typed prefix with the completion text AND insert the import,
// leaving the cursor at the end of the inserted text — shifted down by the line
// the import added above it.
func TestApplyCompletionInsertsAutoImportAndShiftsCursor(t *testing.T) {
	// Line 1 holds the typed prefix "MyCl"; the cursor is just after it.
	m := newCompletionApplyTestModel("const x = 1;\nMyCl\n")
	at := document.Pos{Line: 1, Col: 4}
	m.cursor = at

	item := ClientCompletion{
		Label:      "MyClass",
		InsertText: "MyClass",
		AdditionalEdits: []ClientLspEdit{{
			FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 0,
			NewText: "import { MyClass } from \"./m\";\n",
		}},
	}

	res, _ := m.applyCompletionItem(item, at, "MyCl")
	got := res.(Model)

	wantLines := []string{
		"import { MyClass } from \"./m\";",
		"const x = 1;",
		"MyClass",
	}
	for i, want := range wantLines {
		if got.buf.Line(i) != want {
			t.Errorf("line %d = %q, want %q", i, got.buf.Line(i), want)
		}
	}

	// Cursor: original line 1 pushed to line 2 by the import; column at the end
	// of "MyClass" (from=0 + len("MyClass")).
	if got.cursor.Line != 2 || got.cursor.Col != 7 {
		t.Errorf("cursor = %+v, want {Line:2 Col:7}", got.cursor)
	}
}

// TestApplyCompletionNoImportReplacesPrefix verifies the ordinary path (no
// additionalTextEdits): the typed prefix is replaced in place and the cursor
// lands at the end of the inserted text on the same line.
func TestApplyCompletionNoImportReplacesPrefix(t *testing.T) {
	m := newCompletionApplyTestModel("foo.ba\n")
	at := document.Pos{Line: 0, Col: 6} // just after "ba"
	m.cursor = at

	item := ClientCompletion{Label: "bar", InsertText: "bar"}

	res, _ := m.applyCompletionItem(item, at, "ba")
	got := res.(Model)

	if got.buf.Line(0) != "foo.bar" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "foo.bar")
	}
	if got.cursor.Line != 0 || got.cursor.Col != 7 {
		t.Errorf("cursor = %+v, want {Line:0 Col:7}", got.cursor)
	}
}

// TestApplyCompletionAddsParensForFunction verifies a function completion also
// inserts () with the cursor placed between them.
func TestApplyCompletionAddsParensForFunction(t *testing.T) {
	m := newCompletionApplyTestModel("greetL\n")
	at := document.Pos{Line: 0, Col: 6}
	m.cursor = at
	item := ClientCompletion{Label: "greetLoudly", InsertText: "greetLoudly", Kind: 3} // Function
	res, _ := m.applyCompletionItem(item, at, "greetL")
	got := res.(Model)
	if got.buf.Line(0) != "greetLoudly()" {
		t.Errorf("line 0 = %q, want greetLoudly()", got.buf.Line(0))
	}
	if got.cursor.Line != 0 || got.cursor.Col != 12 {
		t.Errorf("cursor = %+v, want between parens {Line:0 Col:12}", got.cursor)
	}
}

// TestApplyCompletionSkipsParensWhenAlreadyFollowed verifies parens aren't
// doubled when a '(' already follows the cursor.
func TestApplyCompletionSkipsParensWhenAlreadyFollowed(t *testing.T) {
	m := newCompletionApplyTestModel("greetL()\n")
	at := document.Pos{Line: 0, Col: 6} // cursor after greetL, before "("
	m.cursor = at
	item := ClientCompletion{Label: "greetLoudly", InsertText: "greetLoudly", Kind: 3}
	res, _ := m.applyCompletionItem(item, at, "greetL")
	got := res.(Model)
	if got.buf.Line(0) != "greetLoudly()" {
		t.Errorf("line 0 = %q, want greetLoudly() (no doubled parens)", got.buf.Line(0))
	}
	if got.cursor.Col != 11 {
		t.Errorf("cursor col = %d, want 11 (before the existing paren)", got.cursor.Col)
	}
}

// TestApplyCompletionNoParensForVariable verifies non-callable kinds don't get
// parentheses.
func TestApplyCompletionNoParensForVariable(t *testing.T) {
	m := newCompletionApplyTestModel("fo\n")
	at := document.Pos{Line: 0, Col: 2}
	m.cursor = at
	item := ClientCompletion{Label: "foo", InsertText: "foo", Kind: 6} // Variable
	res, _ := m.applyCompletionItem(item, at, "fo")
	got := res.(Model)
	if got.buf.Line(0) != "foo" {
		t.Errorf("line 0 = %q, want foo (no parens for a variable)", got.buf.Line(0))
	}
	if got.cursor.Col != 3 {
		t.Errorf("cursor col = %d, want 3", got.cursor.Col)
	}
}

// TestApplyCompletionUsesTextEditRange verifies the server textEdit is used as
// the authoritative primary edit: completing mid-word (cursor after "greet" in
// "greetLoudly") must replace the whole identifier, not just the prefix before
// the cursor — which would leave the "Loudly" suffix behind.
func TestApplyCompletionUsesTextEditRange(t *testing.T) {
	m := newCompletionApplyTestModel("greetLoudly\n")
	at := document.Pos{Line: 0, Col: 5} // mid-word, after "greet"
	m.cursor = at
	item := ClientCompletion{
		Label: "greetLoudly", InsertText: "greetLoudly", Kind: 6, // Variable (no parens)
		TextEdit: &ClientLspEdit{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 11, NewText: "greetLoudly"},
	}
	res, _ := m.applyCompletionItem(item, at, "greet")
	got := res.(Model)
	if got.buf.Line(0) != "greetLoudly" {
		t.Errorf("line 0 = %q, want greetLoudly (textEdit range replaces the whole word)", got.buf.Line(0))
	}
	if got.cursor.Col != 11 {
		t.Errorf("cursor col = %d, want 11 (end of replacement)", got.cursor.Col)
	}
}

// TestCompletionResolvedIgnoredOnBufferOrCursorChange verifies the deferred
// resolve result is dropped when the buffer switched or the cursor moved since
// acceptance, rather than applying a stale edit to the wrong place.
func TestCompletionResolvedIgnoredOnBufferOrCursorChange(t *testing.T) {
	base := func() Model {
		m := newCompletionApplyTestModel("greetL\n")
		m.bufID = 1
		m.cursor = document.Pos{Line: 0, Col: 6}
		return m
	}
	item := ClientCompletion{Label: "greetLoudly", InsertText: "greetLoudly"}
	accepted := document.Pos{Line: 0, Col: 6}

	// Different buffer → ignored.
	res, _ := base().Update(completionResolvedMsg{item: item, at: accepted, prefix: "greetL", bufID: 2})
	if got := res.(Model); got.buf.Line(0) != "greetL" {
		t.Errorf("applied despite bufID mismatch: line 0 = %q", got.buf.Line(0))
	}

	// Cursor moved since acceptance → ignored.
	m := base()
	m.cursor = document.Pos{Line: 0, Col: 3}
	res2, _ := m.Update(completionResolvedMsg{item: item, at: accepted, prefix: "greetL", bufID: 1})
	if got := res2.(Model); got.buf.Line(0) != "greetL" {
		t.Errorf("applied despite cursor move: line 0 = %q", got.buf.Line(0))
	}

	// Matching buffer + cursor → applied.
	res3, _ := base().Update(completionResolvedMsg{item: item, at: accepted, prefix: "greetL", bufID: 1})
	if got := res3.(Model); got.buf.Line(0) != "greetLoudly" {
		t.Errorf("not applied for matching buffer/cursor: line 0 = %q", got.buf.Line(0))
	}
}
