package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

func newJSDocTestModel(t *testing.T, content string) Model {
	t.Helper()
	m := newTestModel(content)
	m.buf = document.New("test.ts", content)
	m.filePath = "test.ts"
	m.hlr = highlight.New("test.ts")
	if m.hlr == nil {
		t.Skip("no TypeScript highlighter registered; run with -tags lang_all (or lang_typescript)")
	}
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial
	return m
}

// TestJSDocExpandsOnEnterAboveFunction is the primary path: cursor at the
// end of a lone "/**" line directly above a function declaration, Enter
// pressed — the "/**" line should expand into a full JSDoc block with one
// @param per parameter, matching VS Code's trigger.
func TestJSDocExpandsOnEnterAboveFunction(t *testing.T) {
	m := newJSDocTestModel(t, "/**\nfunction add(a: number, b: string): boolean {\n  return true;\n}\n")
	m.cursor = document.Pos{Line: 0, Col: 3} // end of "/**"

	m2, _ := m.handleEnter()
	got := m2

	want := "/**\n * \n * @param {number} a\n * @param {string} b\n * @returns {boolean}\n */\nfunction add(a: number, b: string): boolean {\n  return true;\n}\n"
	if got.buf.Content() != want {
		t.Fatalf("content after JSDoc expansion:\n%s\nwant:\n%s", got.buf.Content(), want)
	}
	// Cursor lands right after " * " on the summary line, ready to type.
	if got.cursor != (document.Pos{Line: 1, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:3}", got.cursor)
	}
}

// TestJSDocDoesNotExpandWithoutFunctionBelow verifies a lone "/**" above
// anything other than a function (or above nothing) just inserts a plain
// newline, like Enter normally would.
func TestJSDocDoesNotExpandWithoutFunctionBelow(t *testing.T) {
	m := newJSDocTestModel(t, "/**\nconst x = 5;\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handleEnter()
	got := m2

	want := "/**\n\nconst x = 5;\n"
	if got.buf.Content() != want {
		t.Fatalf("content = %q, want %q (plain newline, no expansion)", got.buf.Content(), want)
	}
}

// TestJSDocDoesNotExpandMidLine verifies the trigger only fires when Enter is
// pressed at the end of a lone "/**" line, not e.g. after "/**x" or with the
// cursor mid-line.
func TestJSDocDoesNotExpandMidLine(t *testing.T) {
	m := newJSDocTestModel(t, "/**x\nfunction add(a: number) {\n}\n")
	m.cursor = document.Pos{Line: 0, Col: 4} // end of "/**x"

	m2, _ := m.handleEnter()
	got := m2

	want := "/**x\n\nfunction add(a: number) {\n}\n"
	if got.buf.Content() != want {
		t.Fatalf("content = %q, want %q (plain newline, no expansion)", got.buf.Content(), want)
	}
}

// TestJSDocDoesNotExpandOnGoFiles is a regression guard: Go's
// function_declaration node type collides with tsFunctionTypes (shared
// across languages in the highlight package), but Go uses "//" doc comments,
// not "/** */" — the client-side jsdocExtensions gate must keep this from
// firing on a .go file.
func TestJSDocDoesNotExpandOnGoFiles(t *testing.T) {
	m := newTestModel("/**\nfunc add(a, b int) int {\n\treturn a + b\n}\n")
	m.filePath = "test.go"
	m.hlr = highlight.New("test.go")
	if m.hlr == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handleEnter()
	got := m2

	want := "/**\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n"
	if got.buf.Content() != want {
		t.Fatalf("content = %q, want %q (no JSDoc expansion on a .go file)", got.buf.Content(), want)
	}
}

// TestJSDocDoesNotExpandAboveCallbackArgument is a regression test: a "/**"
// line directly above a callback passed as a call argument (arr.map(x =>
// ...)) must not expand — see the matching highlight-package test for why
// (findFunctionOnRow's declaration-context check).
func TestJSDocDoesNotExpandAboveCallbackArgument(t *testing.T) {
	m := newJSDocTestModel(t, "/**\narr.map(x => x + 1);\n")
	m.cursor = document.Pos{Line: 0, Col: 3}

	m2, _ := m.handleEnter()
	got := m2

	want := "/**\n\narr.map(x => x + 1);\n"
	if got.buf.Content() != want {
		t.Fatalf("content = %q, want %q (plain newline, no expansion)", got.buf.Content(), want)
	}
}
