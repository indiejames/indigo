//go:build lang_python || lang_all

package highlight

import "testing"

// TestPythonFStringInterpolationHighlightsAsVariable guards two bugs found
// together while fixing Python f-string interpolation highlighting:
//
//  1. lang_python.go used to take go-sitter-forest's default (nvimts)
//     python query, which resets f-string interpolation highlighting via
//     "(interpolation) @none" but otherwise still captures the whole
//     `string` node (including interpolations) as @string; combined with
//     indigo's flat-priority span resolution (see captureANSI), the
//     higher-priority @string span always won over the lower-priority
//     @variable span an interpolated identifier also received.
//  2. The same nvimts query gates several rules (e.g. "(identifier) @type"
//     for capitalized names) behind Neovim-specific "#lua-match?"
//     predicates, which go-tree-sitter-bare accepts at compile time but
//     never enforces (only "#match?" and a handful of others are wired
//     up) -- so those rules matched every identifier unconditionally,
//     regardless of case, and "@constant.builtin"'s higher priority than
//     "@variable" won for every plain identifier in the file, not just
//     ones inside f-strings.
//
// Switching to the native tree-sitter-python query (python.NativeFirst)
// avoids (2), and patchPythonHighlightQuery's narrower @string capture
// avoids (1).
func TestPythonFStringInterpolationHighlightsAsVariable(t *testing.T) {
	h := New("x.py")
	if h == nil {
		t.Fatal("New(x.py) = nil")
	}
	src := []byte("print(f\"Hello, {name}! Next year you will be {age + 1}.\")\n")
	spans := h.Highlight(src)
	line := spans[0]
	if len(line) == 0 {
		t.Fatal("no spans on line 0")
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

	// "name" spans columns [16,20) and "age" spans [46,49) in
	// `print(f"Hello, {name}! Next year you will be {age + 1}.")`.
	for _, tc := range []struct {
		label string
		col   int
	}{
		{"name", 16},
		{"age", 46},
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

	// Sanity check: the literal text is still highlighted as a string, and
	// a plain lowercase identifier elsewhere doesn't get a bogus
	// higher-priority capture (regression guard for bug 2 above).
	litCol := 9 // inside "Hello, "
	gotLit, ok := first(litCol)
	if !ok {
		t.Fatalf("no span covers column %d (literal text)", litCol)
	}
	if gotLit.ANSI != stringANSI {
		t.Errorf("literal f-string text at col %d rendered with ansi %q, want string color %q", litCol, gotLit.ANSI, stringANSI)
	}

	fnCol := 0 // "print"
	gotFn, ok := first(fnCol)
	if !ok {
		t.Fatalf("no span covers column %d (print)", fnCol)
	}
	if gotFn.ANSI == stringANSI {
		t.Errorf("print at col %d rendered with string color %q, want something else", fnCol, gotFn.ANSI)
	}
}
