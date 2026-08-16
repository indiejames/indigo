//go:build lang_typescript || lang_javascript || lang_all

package highlight

import "testing"

func TestFunctionSignatureAtFunctionDeclaration(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nexport function add(a: number, b: string = \"x\"): boolean {\n  return true;\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 2 {
		t.Fatalf("Params = %+v, want 2 entries", sig.Params)
	}
	if sig.Params[0] != (JSDocParam{Name: "a", Type: "number"}) {
		t.Errorf("Params[0] = %+v", sig.Params[0])
	}
	if sig.Params[1] != (JSDocParam{Name: "[b=\"x\"]", Type: "string"}) {
		t.Errorf("Params[1] = %+v", sig.Params[1])
	}
	if sig.ReturnType != "boolean" {
		t.Errorf("ReturnType = %q, want boolean", sig.ReturnType)
	}
}

func TestFunctionSignatureAtVoidReturnOmitted(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nfunction log(msg: string): void {\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if sig.ReturnType != "" {
		t.Errorf("ReturnType = %q, want empty for void", sig.ReturnType)
	}
}

func TestFunctionSignatureAtArrowFunction(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nconst bar = (x: number, y?: string) => {\n  return x;\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 2 {
		t.Fatalf("Params = %+v, want 2 entries", sig.Params)
	}
	if sig.Params[1] != (JSDocParam{Name: "[y]", Type: "string"}) {
		t.Errorf("Params[1] = %+v", sig.Params[1])
	}
}

func TestFunctionSignatureAtMethodDefinition(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nclass Baz {\n  method(a: number, b?: string): void {}\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 2)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 2 {
		t.Fatalf("Params = %+v, want 2 entries", sig.Params)
	}
}

func TestFunctionSignatureAtDestructuredAndRestParams(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nfunction f(a: number, {c, d}: {c: number, d: string}, ...rest: number[]) {\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 3 {
		t.Fatalf("Params = %+v, want 3 entries", sig.Params)
	}
	if sig.Params[1].Name != "{c, d}" {
		t.Errorf("Params[1].Name = %q, want %q", sig.Params[1].Name, "{c, d}")
	}
	if sig.Params[2].Name != "...rest" {
		t.Errorf("Params[2].Name = %q, want %q", sig.Params[2].Name, "...rest")
	}
}

func TestFunctionSignatureAtPlainJS(t *testing.T) {
	h := New("test.js")
	if h == nil {
		t.Skip("no javascript highlighter registered")
	}
	content := []byte("\nfunction foo(a, b = 5) {\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 2 {
		t.Fatalf("Params = %+v, want 2 entries", sig.Params)
	}
	if sig.Params[0] != (JSDocParam{Name: "a"}) {
		t.Errorf("Params[0] = %+v", sig.Params[0])
	}
	if sig.Params[1] != (JSDocParam{Name: "[b=5]"}) {
		t.Errorf("Params[1] = %+v", sig.Params[1])
	}
}

func TestFunctionSignatureAtWrongLineFails(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\n\nfunction foo(a: number) {\n}\n")
	if _, ok := h.FunctionSignatureAt(content, 1); ok {
		t.Error("FunctionSignatureAt on a blank line before the function = true, want false")
	}
}

func TestFunctionSignatureAtGoNotMatched(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no go highlighter registered")
	}
	// Go's function_declaration node type collides with tsFunctionTypes, but
	// FunctionSignatureAt itself doesn't gate by language — that's the
	// client's job (jsdocExtensions). Exercised here just to document the
	// node still parses under the Go grammar without extracting JS-shaped
	// param fields (Go params have no "pattern"/"type" fields, so they fall
	// through jsdocParam's default branch).
	content := []byte("package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n")
	sig, ok := h.FunctionSignatureAt(content, 2)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	_ = sig
}

// TestFunctionSignatureAtCallbackArgumentNotMatched is a regression test for
// a code-review finding: an arrow/function expression passed directly as a
// call argument (arr.map(x => ...), setTimeout(function() {...}, 100)) isn't
// a declaration worth a JSDoc block, and previously matched anyway because
// findFunctionOnRow only checked node type + row, not declaration context.
func TestFunctionSignatureAtCallbackArgumentNotMatched(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	cases := []string{
		"\narr.map(x => x + 1);\n",
		"\nsetTimeout(function() {}, 100);\n",
		"\narr.forEach((item: number) => {\n  console.log(item);\n});\n",
	}
	for _, content := range cases {
		if _, ok := h.FunctionSignatureAt([]byte(content), 1); ok {
			t.Errorf("FunctionSignatureAt(%q, 1) = true, want false (callback argument)", content)
		}
	}
}

// TestFunctionSignatureAtCallbackAssignedToVariableStillMatched confirms the
// callback-argument exclusion doesn't overreach: a function first assigned
// to a variable (even if that variable is later passed as a callback
// somewhere else) is still a genuine declaration.
func TestFunctionSignatureAtCallbackAssignedToVariableStillMatched(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nconst cb = (x: number) => x + 1;\n")
	if _, ok := h.FunctionSignatureAt(content, 1); !ok {
		t.Error("FunctionSignatureAt = false, want true for a variable-assigned function")
	}
}

// TestFunctionSignatureAtParenlessSingleParamArrow is a regression test:
// tree-sitter exposes a parenless single-param arrow's parameter under a
// "parameter" (singular) field, not "parameters" — FunctionSignatureAt
// previously only checked the plural field, silently returning zero params.
func TestFunctionSignatureAtParenlessSingleParamArrow(t *testing.T) {
	h := New("test.ts")
	if h == nil {
		t.Skip("no typescript highlighter registered")
	}
	content := []byte("\nconst double = x => x * 2;\n")
	sig, ok := h.FunctionSignatureAt(content, 1)
	if !ok {
		t.Fatal("FunctionSignatureAt = false, want true")
	}
	if len(sig.Params) != 1 || sig.Params[0].Name != "x" {
		t.Errorf("Params = %+v, want a single param named x", sig.Params)
	}
}
