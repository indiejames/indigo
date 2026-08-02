package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/highlight"
)

// TestBuildSemanticSpansMapsAllowedTypes verifies identifier-ish token types
// are converted to spans at the right position.
func TestBuildSemanticSpansMapsAllowedTypes(t *testing.T) {
	tokens := []ClientSemanticToken{
		{Line: 0, Col: 4, Length: 3, TokenType: "function"},
		{Line: 0, Col: 8, Length: 5, TokenType: "parameter"},
	}
	spans := buildSemanticSpans(tokens)
	if len(spans[0]) != 2 {
		t.Fatalf("line 0 spans = %v, want 2", spans[0])
	}
	if spans[0][0].StartCol != 4 || spans[0][0].EndCol != 7 {
		t.Errorf("function span = %+v, want StartCol=4 EndCol=7", spans[0][0])
	}
	if spans[0][1].StartCol != 8 || spans[0][1].EndCol != 13 {
		t.Errorf("parameter span = %+v, want StartCol=8 EndCol=13", spans[0][1])
	}
}

// TestBuildSemanticSpansDropsUnmappedTypes verifies token types tree-sitter
// already handles well (keyword, string, comment, etc.) are silently dropped,
// not converted to spans — they're deliberately left to tree-sitter.
func TestBuildSemanticSpansDropsUnmappedTypes(t *testing.T) {
	tokens := []ClientSemanticToken{
		{Line: 0, Col: 0, Length: 4, TokenType: "keyword"},
		{Line: 0, Col: 5, Length: 6, TokenType: "string"},
		{Line: 0, Col: 12, Length: 7, TokenType: "comment"},
		{Line: 0, Col: 20, Length: 3, TokenType: "variable"}, // the one allowed type
	}
	spans := buildSemanticSpans(tokens)
	if len(spans[0]) != 1 {
		t.Fatalf("spans = %v, want exactly 1 (only the variable token)", spans[0])
	}
	if spans[0][0].StartCol != 20 {
		t.Errorf("surviving span = %+v, want the variable token at col 20", spans[0][0])
	}
}

// TestBuildSemanticSpansAppliesModifiers verifies modifier SGR codes are
// prefixed onto the base color.
func TestBuildSemanticSpansAppliesModifiers(t *testing.T) {
	tokens := []ClientSemanticToken{
		{Line: 0, Col: 0, Length: 3, TokenType: "variable", Modifiers: []string{"readonly", "static"}},
	}
	spans := buildSemanticSpans(tokens)
	ansi := spans[0][0].ANSI
	if !strings.Contains(ansi, "\x1b[3m") {
		t.Errorf("ANSI %q missing italic (readonly)", ansi)
	}
	if !strings.Contains(ansi, "\x1b[1m") {
		t.Errorf("ANSI %q missing bold (static)", ansi)
	}
}

// TestBuildSemanticSpansEmpty verifies no tokens yields a nil map, not a panic.
func TestBuildSemanticSpansEmpty(t *testing.T) {
	if spans := buildSemanticSpans(nil); spans != nil {
		t.Errorf("buildSemanticSpans(nil) = %v, want nil", spans)
	}
}

// TestBuildSemanticSpansSkipsZeroLength guards against a degenerate zero (or
// negative) length token producing a span with StartCol >= EndCol, which
// would render as invisible but still occupy a slot / could confuse
// downstream span-lookup logic.
func TestBuildSemanticSpansSkipsZeroLength(t *testing.T) {
	tokens := []ClientSemanticToken{{Line: 0, Col: 5, Length: 0, TokenType: "variable"}}
	spans := buildSemanticSpans(tokens)
	if len(spans[0]) != 0 {
		t.Errorf("spans = %v, want none for a zero-length token", spans[0])
	}
}

// TestRenderLineChunkSemanticSpansTakePrecedenceOverTreeSitter is the critical
// correctness test for the render merge: a semantic span must win over a
// tree-sitter span covering the same position (e.g. tree-sitter's generic
// "variable" capture vs. the LSP's more specific "parameter" classification),
// while a tree-sitter span with NO semantic counterpart on that line (e.g. a
// keyword) is untouched.
func TestRenderLineChunkSemanticSpansTakePrecedenceOverTreeSitter(t *testing.T) {
	m := newTestModel("func f(x int) {}\n")
	const treeSitterColor = "\x1b[38;2;1;1;1m"
	const semanticColor = "\x1b[38;2;2;2;2m"
	m.hlSpans = highlight.LineSpans{
		0: {
			{StartCol: 0, EndCol: 4, ANSI: treeSitterColor}, // "func" — no semantic counterpart
			{StartCol: 7, EndCol: 8, ANSI: treeSitterColor}, // "x" — tree-sitter's generic guess
		},
	}
	m.semanticSpans = highlight.LineSpans{
		0: {
			{StartCol: 7, EndCol: 8, ANSI: semanticColor}, // "x" — semantic says "parameter"
		},
	}
	cw := 80
	layout := m.buildScreenLayout(1, cw)
	rendered := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

	// "x" at col 7 must carry the semantic color, not tree-sitter's.
	xIdx := strings.Index(rendered, "x")
	if xIdx < 0 {
		t.Fatal("rendered line doesn't contain 'x'")
	}
	before := rendered[:xIdx]
	lastColorBeforeX := ""
	for _, color := range []string{treeSitterColor, semanticColor} {
		if idx := strings.LastIndex(before, color); idx >= 0 {
			if lastIdx := strings.LastIndex(before, lastColorBeforeX); lastColorBeforeX == "" || idx > lastIdx {
				lastColorBeforeX = color
			}
		}
	}
	if lastColorBeforeX != semanticColor {
		t.Errorf("expected semantic color to win for 'x', rendered = %q", rendered)
	}
	// "func" must still carry tree-sitter's color (no semantic span for it).
	if !strings.Contains(rendered, treeSitterColor) {
		t.Errorf("expected tree-sitter color to still apply to 'func', rendered = %q", rendered)
	}
}
