//go:build lang_typescript || lang_all

package highlight

import "testing"

// firstSpanAt mirrors internal/client/render.go's renderLineRunes.spanIdxAt:
// spans are sorted highest-priority-first, and the first span covering a
// column is the one that actually renders.
func firstSpanAt(spans []Span, col int) (Span, bool) {
	for _, s := range spans {
		if col >= s.StartCol && col < s.EndCol {
			return s, true
		}
	}
	return Span{}, false
}

// TestTSTemplateLiteralInterpolationHighlightsAsVariable guards against a
// regression where the (template_string) node was captured as @string in
// its entirety, so the higher-priority string color always outranked the
// interior @variable/@punctuation.special captures for a ${...}
// interpolation's contents (see query_ecma.go's template_string pattern).
func TestTSTemplateLiteralInterpolationHighlightsAsVariable(t *testing.T) {
	h := New("x.ts")
	if h == nil {
		t.Fatal("New(x.ts) = nil")
	}
	src := []byte("console.log(`Hello, ${name}`);\n")
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

	// "name" spans columns [22,26) in `console.log(\`Hello, ${name}\`);`.
	nameCol := 22
	got, ok := firstSpanAt(line, nameCol)
	if !ok {
		t.Fatalf("no span covers column %d (%q)", nameCol, "name")
	}
	if got.ANSI != variableANSI {
		t.Errorf("interpolated identifier at col %d rendered with ansi %q, want variable color %q (got string color? %v)",
			nameCol, got.ANSI, variableANSI, got.ANSI == stringANSI)
	}

	// Sanity check: the literal text before the interpolation is still
	// highlighted as a string.
	litCol := 14 // inside "Hello, "
	gotLit, ok := firstSpanAt(line, litCol)
	if !ok {
		t.Fatalf("no span covers column %d (literal text)", litCol)
	}
	if gotLit.ANSI != stringANSI {
		t.Errorf("literal template text at col %d rendered with ansi %q, want string color %q", litCol, gotLit.ANSI, stringANSI)
	}
}

// TestTSTemplateLiteralBraceNotExpandedAtInterpolationBoundary guards
// against a regression from the interpolation-highlighting change above:
// query_ecma.go's template_string pattern now captures each backtick
// delimiter and string_fragment as its own separate "string" span (instead
// of the whole template_string node as one span), so a cursor sitting
// exactly on the seam between two of those adjacent sub-spans — e.g. right
// after a "$" about to become "${", or right after a substitution's
// closing "}" about to start a new one — could fall through IsInString's
// deliberately-exclusive endpoint check and be misclassified as "outside"
// the string, which in turn made ShouldExpandBraceBlock treat the '{' that
// opens a new interpolation as an ordinary block-opening brace and expand
// it onto three lines instead of leaving it as inline "${}" content.
func TestTSTemplateLiteralBraceNotExpandedAtInterpolationBoundary(t *testing.T) {
	h := New("x.ts")
	if h == nil {
		t.Fatal("New(x.ts) = nil")
	}

	tests := []struct {
		name    string
		content string
		col     int
	}{
		{
			name:    "cursor right after $ in a fresh template literal",
			content: "const s = `hello $`;\n",
			col:     len("const s = `hello $"),
		},
		{
			name:    "cursor right after $ starting a second interpolation",
			content: "const s = `${a}$`;\n",
			col:     len("const s = `${a}$"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.content)
			if !h.IsInString(content, 0, tt.col) {
				t.Errorf("IsInString(col=%d) = false, want true (cursor sits between $ and the next string content)", tt.col)
			}
			if h.ShouldExpandBraceBlock(content, 0, tt.col) {
				t.Errorf("ShouldExpandBraceBlock(col=%d) = true, want false (typing '{' here starts a ${...} interpolation, not a block)", tt.col)
			}
		})
	}
}

// TestTSTemplateLiteralBraceStillExpandsInsideInterpolation confirms the
// fix above doesn't overcorrect: a '{' typed inside an already-open
// ${...} interpolation is ordinary code (e.g. an IIFE's function body),
// not string content, and should still be free to expand into a block.
func TestTSTemplateLiteralBraceStillExpandsInsideInterpolation(t *testing.T) {
	h := New("x.ts")
	if h == nil {
		t.Fatal("New(x.ts) = nil")
	}
	content := []byte("const s = `${(() => \n)()}`;\n")
	col := len("const s = `${(() => ")
	if h.IsInString(content, 0, col) {
		t.Errorf("IsInString(col=%d) = true, want false (position is inside the ${...} expression, not string content)", col)
	}
}
