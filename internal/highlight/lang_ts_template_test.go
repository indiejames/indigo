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
