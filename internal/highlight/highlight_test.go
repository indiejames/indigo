package highlight

import "testing"

func TestNewUnknownExtension(t *testing.T) {
	for _, path := range []string{"file.xyz", "file.unknown", "noextension", ""} {
		if h := New(path); h != nil {
			t.Errorf("New(%q) = non-nil, want nil", path)
		}
	}
}

// TestHexToANSIMalformedInputDoesNotPanic is a regression test: a theme
// file with a malformed color (short hex, non-hex characters, empty
// string) must degrade to the terminal's default foreground instead of
// panicking the editor on startup via a slice-bounds-out-of-range.
func TestHexToANSIMalformedInputDoesNotPanic(t *testing.T) {
	for _, hex := range []string{"#fff", "#12345", "#gggggg", "red", "", "#"} {
		got := hexToANSI(hex)
		if got != defaultFgANSI {
			t.Errorf("hexToANSI(%q) = %q, want default fallback %q", hex, got, defaultFgANSI)
		}
	}
}

func TestHexToANSIValidInput(t *testing.T) {
	got := hexToANSI("#569CD6")
	want := "\x1b[38;2;86;156;214m"
	if got != want {
		t.Errorf("hexToANSI(#569CD6) = %q, want %q", got, want)
	}
}

func TestCaptureANSIFallsBackToMostSpecificEntry(t *testing.T) {
	// "function.method" is a real captureTable entry; the fallback for
	// "function.method.private" (not itself in the table) must land there
	// rather than skipping straight to the more generic "function" entry.
	want, wantPrio, ok := captureANSI("function.method")
	if !ok {
		t.Fatal("captureTable has no \"function.method\" entry — test assumption invalid")
	}
	got, gotPrio, ok := captureANSI("function.method.private")
	if !ok {
		t.Fatal("captureANSI(\"function.method.private\") = not found, want a fallback match")
	}
	if got != want {
		t.Errorf("captureANSI(\"function.method.private\") ansi = %q, want %q (the \"function.method\" color)", got, want)
	}
	if gotPrio != wantPrio-1 {
		t.Errorf("captureANSI(\"function.method.private\") priority = %d, want %d (one below \"function.method\")", gotPrio, wantPrio-1)
	}
}

func TestLineCommentPrefixForKey(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"py", "#"},         // bare extension key, needs the "." prefix retry
		{".py", "#"},        // dotted form works directly
		{"PY", "#"},         // case-insensitive
		{"dockerfile", "#"}, // filename-style key, no "." retry needed
		{"not-a-real-language", "//"},
	}
	for _, c := range cases {
		if got := LineCommentPrefixForKey(c.key); got != c.want {
			t.Errorf("LineCommentPrefixForKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestHighlightNilSafe(t *testing.T) {
	var h *Highlighter
	if spans := h.Highlight([]byte("anything")); spans != nil {
		t.Errorf("nil Highlighter.Highlight() = %v, want nil", spans)
	}
}

// rawWinnerAt mirrors renderLineRunes' spanIdxAt: the first span (post-sort)
// whose range covers col.
func rawWinnerAt(spans []rawSpan, col int) (rawSpan, bool) {
	for _, s := range spans {
		if col >= s.startCol && col < s.endCol {
			return s, true
		}
	}
	return rawSpan{}, false
}

// TestSortSpansByNestingHandlesNestedAndDisjointSpans is a regression test
// for a real cycle in the priority-only-among-disjoint-spans, containment-
// among-nested-spans comparator this replaced: with A (cols 0-10, priority
// 100) containing B (cols 2-5, priority 70), and a third span C (cols
// 15-20, priority 90) disjoint from both, the old pairwise comparator gave
// B < A (nesting), A < C (priority, 100 > 90), and C < B (priority, 90 >
// 70) — a cycle (B < A < C < B) that made sort.SliceStable's result depend
// on implementation details and input order rather than being well-defined.
// Depth-based ordering has no such cycle: B (nested one level) always sorts
// before both top-level spans, and A/C (both depth 0) fall back to
// priority.
func TestSortSpansByNestingHandlesNestedAndDisjointSpans(t *testing.T) {
	a := rawSpan{line: 0, startCol: 0, endCol: 10, ansi: "A", priority: 100}
	b := rawSpan{line: 0, startCol: 2, endCol: 5, ansi: "B", priority: 70}
	c := rawSpan{line: 0, startCol: 15, endCol: 20, ansi: "C", priority: 90}

	orderings := [][]rawSpan{
		{a, b, c},
		{b, a, c},
		{c, b, a},
		{b, c, a},
		{a, c, b},
		{c, a, b},
	}
	for _, spans := range orderings {
		spans := append([]rawSpan(nil), spans...)
		sortSpansByNesting(spans)

		// Inside B's range, the nested span must win over its ancestor A.
		if w, ok := rawWinnerAt(spans, 3); !ok || w.ansi != "B" {
			t.Errorf("input order %v: winner at col 3 = %+v, want B", spanAnsis(spans), w)
		}
		// Inside A's range but outside B, A is the only span covering it.
		if w, ok := rawWinnerAt(spans, 7); !ok || w.ansi != "A" {
			t.Errorf("input order %v: winner at col 7 = %+v, want A", spanAnsis(spans), w)
		}
		// C is disjoint from A/B and must win in its own range regardless
		// of A's higher priority.
		if w, ok := rawWinnerAt(spans, 17); !ok || w.ansi != "C" {
			t.Errorf("input order %v: winner at col 17 = %+v, want C", spanAnsis(spans), w)
		}
	}
}

func spanAnsis(spans []rawSpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.ansi
	}
	return out
}

func TestHighlightEmptyContent(t *testing.T) {
	var h *Highlighter
	if spans := h.Highlight([]byte{}); spans != nil {
		t.Error("nil Highlighter.Highlight(empty) should return nil")
	}
}

func TestIsInStringNilSafe(t *testing.T) {
	var h *Highlighter
	if h.IsInString([]byte("anything"), 0, 0) {
		t.Error("nil Highlighter.IsInString() = true, want false")
	}
}

func TestIsInString(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}

	content := []byte(`package main

var s = "hello world"
var n = 1
`)
	tests := []struct {
		name       string
		line, col  int
		wantString bool
	}{
		{"inside string body", 2, 12, true},
		{"just inside opening quote", 2, 9, true},
		{"just before closing quote", 2, 20, true},
		{"right before opening quote (boundary, outside)", 2, 8, false},
		{"right after closing quote (boundary, outside)", 2, 21, false},
		{"well before string", 2, 5, false},
		{"unrelated line", 3, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.IsInString(content, tt.line, tt.col); got != tt.wantString {
				t.Errorf("IsInString(line=%d, col=%d) = %v, want %v", tt.line, tt.col, got, tt.wantString)
			}
		})
	}
}
