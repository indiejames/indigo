package server

import "testing"

// TestUTF16ColToRune verifies the UTF-16 code-unit-offset to rune-index
// conversion used for LSP inlay hint positions. LSP Position.character is a
// UTF-16 code-unit offset by default (indigo never negotiates an alternate
// positionEncoding), but indigo treats every column elsewhere as a rune
// index. Runes outside the Basic Multilingual Plane (U+10000+, e.g. most
// emoji) encode as two UTF-16 code units but remain a single rune, so a
// direct offset-as-rune-index assumption drifts by one for each such rune
// before the target position.
func TestUTF16ColToRune(t *testing.T) {
	tests := []struct {
		name string
		line string
		col  int
		want int
	}{
		{"ASCII: no drift", "let x = bar(1, 2)", 12, 12},
		{"BMP non-ASCII (café): 1 UTF-16 unit == 1 rune, no drift", "café()", 5, 5},
		// "😀" (U+1F600) is outside the BMP: 2 UTF-16 code units, 1 rune.
		// Runes: f(0) o(1) o(2) ((3) 😀(4) )(5) — 6 runes total.
		// UTF-16 units: f(0) o(1) o(2) ((3) 😀(4,5) )(6) — 7 units total.
		{"non-BMP emoji before target: UTF-16 col 6 (')') maps to rune 5", "foo(😀)", 6, 5},
		{"non-BMP emoji: UTF-16 col 4 (right after emoji start) maps to rune 4", "foo(😀)", 4, 4},
		{"col at end of line clamps to len(line)", "abc", 3, 3},
		{"col past end of line clamps to len(line)", "abc", 99, 3},
		{"col zero", "abc", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := utf16ColToRune([]rune(tt.line), tt.col)
			if got != tt.want {
				t.Errorf("utf16ColToRune(%q, %d) = %d, want %d", tt.line, tt.col, got, tt.want)
			}
		})
	}
}

// TestUTF16ColToRuneTokenLengthAcrossNonBMPRune verifies the composition used
// by SemanticTokensRange to convert a token's length: calling utf16ColToRune
// at both the token's start and its end (start+length in UTF-16 units), then
// taking the difference. This must be correct not just when a non-BMP rune
// precedes the token (covered by the table above), but when the token itself
// IS or contains one — naively reusing the UTF-16 length as a rune count
// would overcount by one per such rune, causing the renderer to consume one
// extra real character past the token as part of its coloring.
func TestUTF16ColToRuneTokenLengthAcrossNonBMPRune(t *testing.T) {
	// "foo(😀)" — runes: f(0) o(1) o(2) ((3) 😀(4) )(5). UTF-16 units:
	// f(0) o(1) o(2) ((3) 😀(4,5) )(6). A token spanning exactly the emoji is
	// UTF-16 [4,6) — length 2 units — but must convert to rune [4,5) — length
	// 1 rune, since 😀 is a single rune.
	line := []rune("foo(😀)")
	startUTF16, endUTF16 := 4, 6

	runeStart := utf16ColToRune(line, startUTF16)
	runeEnd := utf16ColToRune(line, endUTF16)
	length := runeEnd - runeStart

	if runeStart != 4 {
		t.Errorf("runeStart = %d, want 4", runeStart)
	}
	if length != 1 {
		t.Errorf("length = %d, want 1 (the emoji is one rune, not two)", length)
	}
}
