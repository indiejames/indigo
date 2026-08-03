//go:build lang_all

package highlight

import "testing"

func TestDedentTargetGoNestedBlock(t *testing.T) {
	h := New("test.go")
	content := "func foo() {\n\tif x {\n\t\tbar()\n\t}\n}\n"
	// Cursor right before the '}' that closes "if x {" (line 3, after the tab).
	got, ok := h.DedentTarget([]byte(content), 3, 1)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true")
	}
	if got != "\t" {
		t.Errorf("DedentTarget = %q, want %q (matches \"if x {\"'s indent)", got, "\t")
	}
}

func TestDedentTargetGoTopLevelBlock(t *testing.T) {
	h := New("test.go")
	content := "func foo() {\n\tdoStuff()\n}\n"
	got, ok := h.DedentTarget([]byte(content), 2, 0)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true")
	}
	if got != "" {
		t.Errorf("DedentTarget = %q, want empty (func foo() starts at col 0)", got)
	}
}

func TestDedentTargetRustNestedBlock(t *testing.T) {
	h := New("test.rs")
	content := "fn foo() {\n    if x {\n        bar();\n    }\n}\n"
	got, ok := h.DedentTarget([]byte(content), 3, 4)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true")
	}
	if got != "    " {
		t.Errorf("DedentTarget = %q, want %q", got, "    ")
	}
}

func TestDedentTargetCNestedBlock(t *testing.T) {
	h := New("test.c")
	content := "int foo() {\n    if (x) {\n        bar();\n    }\n}\n"
	got, ok := h.DedentTarget([]byte(content), 3, 4)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true")
	}
	if got != "    " {
		t.Errorf("DedentTarget = %q, want %q", got, "    ")
	}
}

func TestDedentTargetPythonListLiteral(t *testing.T) {
	h := New("test.py")
	content := "x = [\n    1,\n    2,\n]\n"
	got, ok := h.DedentTarget([]byte(content), 3, 0)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true")
	}
	if got != "" {
		t.Errorf("DedentTarget = %q, want empty (\"x = [\" starts at col 0)", got)
	}
}

func TestDedentTargetNoMatchWhenNotBeforeCloser(t *testing.T) {
	h := New("test.go")
	content := "func foo() {\n\tdoStuff()\n}\n"
	// Cursor at the start of "doStuff()" — nothing closing right there.
	if _, ok := h.DedentTarget([]byte(content), 1, 0); ok {
		t.Error("DedentTarget: got ok=true, want false (cursor isn't before a closer)")
	}
}

func TestDedentTargetNegativeColDoesNotPanic(t *testing.T) {
	h := New("test.go")
	content := "func foo() {\n\tdoStuff()\n}\n"
	if _, ok := h.DedentTarget([]byte(content), 1, -1); ok {
		t.Error("DedentTarget: got ok=true for negative col, want false")
	}
}

func TestDedentTargetHandlesMultiByteRunesBeforeCloser(t *testing.T) {
	// The line with the closer has a multi-byte rune (文, 3 UTF-8 bytes)
	// before it. cursor.Col is rune-based (8: right before '}'), but
	// tree-sitter reports byte-based columns — the closer's real byte
	// column is 10, not 8. Without converting rune col to byte col first,
	// the position comparison inside DedentTarget would never match.
	h := New("test.go")
	content := "func foo() {\n\tif x {\n\t\t文bar()}\n\t}\n}\n"
	got, ok := h.DedentTarget([]byte(content), 2, 8)
	if !ok {
		t.Fatal("DedentTarget: got ok=false, want true (multi-byte rune before the closer)")
	}
	if got != "\t" {
		t.Errorf("DedentTarget = %q, want %q", got, "\t")
	}
}

func TestDedentTargetJSStubIsInertNotCrashing(t *testing.T) {
	// javascript's vendored indents.scm is "; inherits: ecma,jsx" with no
	// local captures, and this binding doesn't resolve "inherits". This
	// locks in that the language is registered but the feature is simply a
	// no-op for it, rather than silently misbehaving or panicking.
	h := New("test.js")
	if h == nil {
		t.Fatal("nil highlighter for test.js")
	}
	content := "function foo() {\n\tdoStuff()\n}\n"
	if _, ok := h.DedentTarget([]byte(content), 2, 0); ok {
		t.Error("DedentTarget: got ok=true for JS, want false (no indent query registered)")
	}
}
