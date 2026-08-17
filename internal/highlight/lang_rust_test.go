//go:build lang_rust || lang_all

package highlight

import "testing"

// TestRustFormatMacroInterpolationHighlightsAsVariable guards Rust's
// captured-identifier format-string syntax (`println!("{name}")`,
// stabilized in Rust 2021). Unlike JS template literals or Python
// f-strings, tree-sitter-rust's grammar has no syntax node for this at
// all -- `{name}` is just characters inside an opaque string_content node,
// because format-string parsing is a macro-level convention, not part of
// the language grammar (confirmed against go-sitter-forest's rust grammar,
// and upstream nvim-treesitter's own rust queries don't handle this
// either). So this is covered by Highlighter.postProcess
// (rustFormatArgSpans in lang_rust.go), a hand-rolled scanner over
// recognized formatting-macro string literals, not a query tweak.
func TestRustFormatMacroInterpolationHighlightsAsVariable(t *testing.T) {
	h := New("main.rs")
	if h == nil {
		t.Fatal("New(main.rs) = nil")
	}
	src := []byte("fn main() {\n" +
		"    let name = \"Rust\";\n" +
		"    let version = 1.0;\n" +
		"    println!(\"Hello, {name} version {version}!\");\n" +
		"}\n")
	spans := h.Highlight(src)
	line := spans[3]
	if len(line) == 0 {
		t.Fatal("no spans on line 3")
	}

	stringANSI, _, ok := captureANSI("string")
	if !ok {
		t.Fatal("captureANSI(string) not found")
	}
	variableANSI, _, ok := captureANSI("variable")
	if !ok {
		t.Fatal("captureANSI(variable) not found")
	}

	first := func(col int) (Span, bool) {
		for _, s := range line {
			if col >= s.StartCol && col < s.EndCol {
				return s, true
			}
		}
		return Span{}, false
	}

	// line 3: `    println!("Hello, {name} version {version}!");`
	// "name" starts at col 22, "version" (the interpolated one) at col 37.
	for _, tc := range []struct {
		label string
		col   int
	}{
		{"name", 22},
		{"version", 37},
	} {
		got, ok := first(tc.col)
		if !ok {
			t.Fatalf("no span covers column %d (%s)", tc.col, tc.label)
		}
		if got.ANSI != variableANSI {
			t.Errorf("%s at col %d rendered with ansi %q, want variable color %q (string color? %v)",
				tc.label, tc.col, got.ANSI, variableANSI, got.ANSI == stringANSI)
		}
	}

	// Sanity check: the literal text is still highlighted as a string.
	litCol := 14 // inside "Hello, "
	gotLit, ok := first(litCol)
	if !ok {
		t.Fatalf("no span covers column %d (literal text)", litCol)
	}
	if gotLit.ANSI != stringANSI {
		t.Errorf("literal format-string text at col %d rendered with ansi %q, want string color %q", litCol, gotLit.ANSI, stringANSI)
	}
}

// TestRustFormatMacroInterpolationEdgeCases covers escaped braces,
// positional/empty arguments, non-format-macro strings, and unrecognized
// macro names -- all of which must NOT produce a variable span.
func TestRustFormatMacroInterpolationEdgeCases(t *testing.T) {
	h := New("main.rs")
	if h == nil {
		t.Fatal("New(main.rs) = nil")
	}
	src := []byte("fn main() {\n" +
		"    println!(\"literal braces {{}} and {{name}} then {name} pos {} idx {0}\");\n" +
		"    let s = \"not a format macro {name}\";\n" +
		"    my_macro!(\"custom {name}\");\n" +
		"}\n")
	spans := h.Highlight(src)
	variableANSI, _, _ := captureANSI("variable")

	first := func(line []Span, col int) (Span, bool) {
		for _, s := range line {
			if col >= s.StartCol && col < s.EndCol {
				return s, true
			}
		}
		return Span{}, false
	}

	line1 := spans[1]
	l1 := `    println!("literal braces {{}} and {{name}} then {name} pos {} idx {0}");`
	realNameCol := indexInLine(l1, "then {name}") + len("then {")
	got, ok := first(line1, realNameCol)
	if !ok || got.ANSI != variableANSI {
		t.Errorf("real {name} interpolation should be variable-colored, got ok=%v %+v", ok, got)
	}

	escapedNameCol := indexInLine(l1, "{{name}}") + 2
	gotEsc, okEsc := first(line1, escapedNameCol)
	if okEsc && gotEsc.ANSI == variableANSI {
		t.Errorf("escaped {{name}} must not be variable-colored, got %+v", gotEsc)
	}

	line2 := spans[2]
	l2 := `    let s = "not a format macro {name}";`
	gotNonMacro, okNonMacro := first(line2, indexInLine(l2, "name"))
	if okNonMacro && gotNonMacro.ANSI == variableANSI {
		t.Errorf("string outside a recognized format macro must not be variable-colored, got %+v", gotNonMacro)
	}

	line3 := spans[3]
	l3 := `    my_macro!("custom {name}");`
	gotUnknownMacro, okUnknownMacro := first(line3, indexInLine(l3, "name"))
	if okUnknownMacro && gotUnknownMacro.ANSI == variableANSI {
		t.Errorf("unrecognized macro must not be variable-colored, got %+v", gotUnknownMacro)
	}
}

func indexInLine(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
