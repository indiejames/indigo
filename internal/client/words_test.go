package client

import "testing"

func TestIsWordChar(t *testing.T) {
	for _, r := range "abcxyzABCXYZ0123456789_" {
		if !isWordChar(r) {
			t.Errorf("isWordChar(%q) = false, want true", r)
		}
	}
	for _, r := range " \t\n.,;:!@#-+" {
		if isWordChar(r) {
			t.Errorf("isWordChar(%q) = true, want false", r)
		}
	}
}

func TestFindWordAt(t *testing.T) {
	// findWordAt starts from col (does NOT scan backward to word start).
	// "hello world": h(0)e(1)l(2)l(3)o(4) (5)w(6)o(7)r(8)l(9)d(10)
	tests := []struct {
		line      string
		col       int
		wantStart int
		wantEnd   int
		wantFound bool
	}{
		{"hello world", 0, 0, 4, true},    // start of "hello"
		{"hello world", 3, 3, 4, true},    // mid-word: starts from col 3
		{"hello world", 4, 4, 4, true},    // last char of "hello"
		{"hello world", 5, 6, 10, true},   // on space → skips to "world"
		{"hello world", 6, 6, 10, true},   // start of "world"
		{"hello world", 10, 10, 10, true}, // last char of "world"
		{"   spaces", 0, 3, 8, true},      // leading spaces → skips to "spaces"
		{"", 0, -1, -1, false},
		{"   ", 0, -1, -1, false}, // only spaces
	}
	for _, tt := range tests {
		runes := []rune(tt.line)
		s, e, found := findWordAt(runes, tt.col)
		if found != tt.wantFound || s != tt.wantStart || e != tt.wantEnd {
			t.Errorf("findWordAt(%q, %d) = (%d, %d, %v), want (%d, %d, %v)",
				tt.line, tt.col, s, e, found, tt.wantStart, tt.wantEnd, tt.wantFound)
		}
	}
}

func TestFindNextWordFrom(t *testing.T) {
	tests := []struct {
		line      string
		afterEnd  int
		wantStart int
		wantEnd   int
		wantFound bool
	}{
		{"hello world", 4, 6, 10, true},       // after "hello" → finds "world"
		{"hello world foo", 10, 12, 14, true}, // after "world" → finds "foo"
		{"hello world", 10, -1, -1, false},    // after last word → not found
		{"hello   world", 4, 8, 12, true},     // gap between words
		{"hello", 4, -1, -1, false},
	}
	for _, tt := range tests {
		runes := []rune(tt.line)
		s, e, found := findNextWordFrom(runes, tt.afterEnd)
		if found != tt.wantFound || s != tt.wantStart || e != tt.wantEnd {
			t.Errorf("findNextWordFrom(%q, %d) = (%d, %d, %v), want (%d, %d, %v)",
				tt.line, tt.afterEnd, s, e, found, tt.wantStart, tt.wantEnd, tt.wantFound)
		}
	}
}

func TestFindPrevWordStart(t *testing.T) {
	// "hello world": 01234 5 678910
	tests := []struct {
		line      string
		col       int
		wantStart int
		wantFound bool
	}{
		{"hello world", 6, 0, true},   // 'w' → start of "hello" (crosses space)
		{"hello world", 8, 6, true},   // mid "world" → start of "world"
		{"hello world", 5, 0, true},   // space → start of "hello"
		{"hello world", 0, -1, false}, // col 0 → nothing before
		{"hello", 3, 0, true},         // mid "hello" → col 0
		{"", 0, -1, false},
		{"  hello", 7, 2, true}, // end of "hello" → start of "hello" (mid-word)
	}
	for _, tt := range tests {
		runes := []rune(tt.line)
		s, found := findPrevWordStart(runes, tt.col)
		if found != tt.wantFound || (found && s != tt.wantStart) {
			t.Errorf("findPrevWordStart(%q, %d) = (%d, %v), want (%d, %v)",
				tt.line, tt.col, s, found, tt.wantStart, tt.wantFound)
		}
	}
}

func TestFindWordEnd(t *testing.T) {
	tests := []struct {
		line      string
		col       int
		wantEnd   int
		wantFound bool
	}{
		{"hello world", 0, 4, true},    // start of "hello" → end of "hello"
		{"hello world", 4, 10, true},   // at end of "hello" → end of "world"
		{"hello world", 5, 10, true},   // on space → end of "world"
		{"hello world", 6, 10, true},   // start of "world" → end of "world"
		{"hello world", 10, -1, false}, // at last word end → no next word
		{"   hello", 0, 7, true},       // leading spaces → end of "hello"
		{"", 0, -1, false},
	}
	for _, tt := range tests {
		runes := []rune(tt.line)
		e, found := findWordEnd(runes, tt.col)
		if found != tt.wantFound || (found && e != tt.wantEnd) {
			t.Errorf("findWordEnd(%q, %d) = (%d, %v), want (%d, %v)",
				tt.line, tt.col, e, found, tt.wantEnd, tt.wantFound)
		}
	}
}
