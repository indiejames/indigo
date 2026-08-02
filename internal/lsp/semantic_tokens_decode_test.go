package lsp

import (
	"reflect"
	"testing"
)

// TestDecodeSemanticTokens verifies the delta-encoding expansion against the
// LSP spec's SemanticTokens format: 5 uint32s per token
// [deltaLine, deltaStartChar, length, tokenType, tokenModifiers], where
// deltaStartChar is relative to the previous token's start only when
// deltaLine is 0 (same line), and absolute otherwise.
func TestDecodeSemanticTokens(t *testing.T) {
	legend := SemanticTokensLegend{
		TokenTypes:     []string{"variable", "type", "function"},
		TokenModifiers: []string{"declaration", "readonly"},
	}
	// Token 0: line 2, col 5, len 3, type "variable", no modifiers.
	// Token 1: same line (delta 0), col 10 (delta 5 from token 0's col 5), len 4, type "type".
	// Token 2: line 5 (delta 3), col 2 (absolute — new line), len 7, type "function",
	//          modifiers bits 0+1 set ("declaration","readonly").
	data := []uint32{
		2, 5, 3, 0, 0,
		0, 5, 4, 1, 0,
		3, 2, 7, 2, 0b11,
	}
	want := []SemanticToken{
		{Line: 2, StartChar: 5, Length: 3, TokenType: "variable", Modifiers: nil},
		{Line: 2, StartChar: 10, Length: 4, TokenType: "type", Modifiers: nil},
		{Line: 5, StartChar: 2, Length: 7, TokenType: "function", Modifiers: []string{"declaration", "readonly"}},
	}
	got := decodeSemanticTokens(data, legend)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeSemanticTokens() = %+v, want %+v", got, want)
	}
}

// TestDecodeSemanticTokensEmpty verifies an empty data array decodes to no tokens.
func TestDecodeSemanticTokensEmpty(t *testing.T) {
	got := decodeSemanticTokens(nil, SemanticTokensLegend{})
	if len(got) != 0 {
		t.Errorf("decodeSemanticTokens(nil) = %v, want empty", got)
	}
}

// TestDecodeSemanticTokensIgnoresMalformedTrailingData verifies a data array
// whose length isn't a multiple of 5 (malformed/truncated) drops the trailing
// partial group instead of panicking on an out-of-bounds index.
func TestDecodeSemanticTokensIgnoresMalformedTrailingData(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"variable"}}
	data := []uint32{0, 0, 3, 0, 0, 1, 2} // one full token + 2 trailing uint32s
	got := decodeSemanticTokens(data, legend)
	if len(got) != 1 {
		t.Fatalf("decodeSemanticTokens() returned %d tokens, want 1 (trailing partial group dropped)", len(got))
	}
	if got[0].TokenType != "variable" {
		t.Errorf("token = %+v, want TokenType=variable", got[0])
	}
}

// TestDecodeSemanticTokensOutOfRangeTypeIndex verifies a tokenType index
// outside the legend's bounds (a mismatched/stale legend) degrades to an
// empty type string rather than panicking.
func TestDecodeSemanticTokensOutOfRangeTypeIndex(t *testing.T) {
	legend := SemanticTokensLegend{TokenTypes: []string{"variable"}}
	data := []uint32{0, 0, 3, 5, 0} // type index 5, legend only has index 0
	got := decodeSemanticTokens(data, legend)
	if len(got) != 1 {
		t.Fatalf("got %d tokens, want 1", len(got))
	}
	if got[0].TokenType != "" {
		t.Errorf("TokenType = %q, want empty for an out-of-range index", got[0].TokenType)
	}
}
