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

// TestSignatureIsCallable checks the resolved-detail heuristic used to add call
// parens when the LSP kind alone doesn't identify a callable (imported
// functions arrive as kind Variable).
func TestSignatureIsCallable(t *testing.T) {
	cases := []struct {
		detail string
		want   bool
	}{
		{"(alias) function greetLoudly(name: string): string\nimport greetLoudly", true},
		{"function foo(): void", true},
		{"(method) Foo.bar(x: number): void", true},
		{"const cb: (x: number) => void", true},
		{"const Ctor: new (x: number) => Foo", false},          // construct signature — new-able, not callable
		{"const Ctor: abstract new (x: number) => Foo", false}, // abstract construct signature
		{"const x: number", false},
		// Fresh (not-yet-imported) auto-import candidate: signature is on the
		// second line, after an "Auto import from '...'" preamble.
		{"Auto import from './helper'\nfunction bar(x: number, y: number): number", true},
		{"const s: string", false},
		{"(property) Foo.count: number", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := signatureIsCallable(tc.detail); got != tc.want {
			t.Errorf("signatureIsCallable(%q) = %v, want %v", tc.detail, got, tc.want)
		}
	}
}

// TestApplyCompletionAddsParensForImportedFunction verifies an already-imported
// function (LSP kind Variable, but a callable resolved detail) still gets call
// parentheses with the cursor placed between them.
func TestApplyCompletionAddsParensForImportedFunction(t *testing.T) {
	m := newCompletionApplyTestModel("greetL\n")
	at := document.Pos{Line: 0, Col: 6}
	m.cursor = at
	item := ClientCompletion{
		Label: "greetLoudly", InsertText: "greetLoudly", Kind: 6, // Variable (import alias)
		Detail: "(alias) function greetLoudly(name: string): string\nimport greetLoudly",
	}
	res, _ := m.applyCompletionItem(item, at, "greetL")
	got := res.(Model)
	if got.buf.Line(0) != "greetLoudly(name)" {
		t.Errorf("line 0 = %q, want greetLoudly(name) (imported function gets named placeholder)", got.buf.Line(0))
	}
	if got.cursor.Col != 12 {
		t.Errorf("cursor col = %d, want 12 (start of the first argument placeholder)", got.cursor.Col)
	}
}

// TestApplyCompletionFillsMultipleArgPlaceholders verifies multi-parameter
// functions insert all parameter names and land the cursor on the first.
func TestApplyCompletionFillsMultipleArgPlaceholders(t *testing.T) {
	m := newCompletionApplyTestModel("gr\n")
	at := document.Pos{Line: 0, Col: 2}
	m.cursor = at
	item := ClientCompletion{
		Label: "greet", InsertText: "greet", Kind: 3,
		Detail: "function greet(name: string, times: number): void",
	}
	res, _ := m.applyCompletionItem(item, at, "gr")
	got := res.(Model)
	if got.buf.Line(0) != "greet(name, times)" {
		t.Errorf("line 0 = %q, want greet(name, times)", got.buf.Line(0))
	}
	if got.cursor.Col != 6 { // start of "name" (g0 r1 e2 e3 t4 (5 n6)
		t.Errorf("cursor col = %d, want 6 (start of first arg)", got.cursor.Col)
	}
}

// TestApplyCompletionFreshAutoImportFillsArgs reproduces the exact bug report:
// accepting a fresh (not-yet-imported) auto-import candidate for a two-argument
// function must still fill in both argument placeholders. This is the real
// resolved-item shape captured from typescript-language-server for a function
// export that isn't imported yet — detail leads with an "Auto import from"
// preamble, and additionalTextEdits appends the name into an existing import
// statement rather than inserting a whole new import line.
func TestApplyCompletionFreshAutoImportFillsArgs(t *testing.T) {
	m := newCompletionApplyTestModel("import { greetLoudly } from \"./helper\";\n\nconst r = bar\n")
	at := document.Pos{Line: 2, Col: 13} // end of "bar"
	m.cursor = at
	item := ClientCompletion{
		Label: "bar", InsertText: "bar", Kind: 3,
		Detail: "Auto import from './helper'\nfunction bar(x: number, y: number): number",
		AdditionalEdits: []ClientLspEdit{{
			FromLine: 0, FromCol: 9, ToLine: 0, ToCol: 9, NewText: "bar, ",
		}},
	}
	res, _ := m.applyCompletionItem(item, at, "bar")
	got := res.(Model)

	if got.buf.Line(0) != "import { bar, greetLoudly } from \"./helper\";" {
		t.Errorf("line 0 = %q, want the import list to gain bar", got.buf.Line(0))
	}
	if got.buf.Line(2) != "const r = bar(x, y)" {
		t.Errorf("line 2 = %q, want const r = bar(x, y) (args filled in)", got.buf.Line(2))
	}
	if !got.snippetOn {
		t.Error("expected snippet mode to be active with argument placeholders")
	}
}

// TestApplyCompletionExtendsStaleTextEditPastNarrowedPrefix is a regression
// test for a real reported bug: in a Go file, typing "m." triggered a
// completion fetch (gopls returns snippetOn's textEdit as the zero-width
// range {3,3} right after the dot — confirmed against a real gopls). The user
// then typed "sn" — indigo's incremental narrowing re-filters the cached list
// locally without re-fetching, so the item's textEdit stayed frozen at {3,3}.
// Accepting "snippetOn" applied that stale zero-width edit, inserting the
// completion BEFORE the already-typed "sn" instead of replacing it, producing
// "m.snippetOnsn" instead of "m.snippetOn". The fix extends a textEdit whose
// end trails behind the current cursor (more was typed after the fetch) to
// also consume those characters.
func TestApplyCompletionExtendsStaleTextEditPastNarrowedPrefix(t *testing.T) {
	m := newCompletionApplyTestModel("m.sn\n")
	at := document.Pos{Line: 0, Col: 4} // cursor after "m.sn"
	m.cursor = at
	item := ClientCompletion{
		Label: "snippetOn", InsertText: "snippetOn", Kind: 5, // Field
		// The stale, zero-width textEdit gopls returned at the ORIGINAL
		// trigger point (right after "m."), before "sn" was typed.
		TextEdit: &ClientLspEdit{FromLine: 0, FromCol: 2, ToLine: 0, ToCol: 2, NewText: "snippetOn"},
	}
	res, _ := m.applyCompletionItem(item, at, "sn")
	got := res.(Model)

	if got.buf.Line(0) != "m.snippetOn" {
		t.Errorf("line 0 = %q, want m.snippetOn (the typed \"sn\" must be consumed, not left over)", got.buf.Line(0))
	}
	if got.cursor.Col != 11 {
		t.Errorf("cursor col = %d, want 11 (end of snippetOn)", got.cursor.Col)
	}
}
