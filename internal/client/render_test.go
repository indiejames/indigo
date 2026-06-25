package client

import "testing"

func TestExpandTabsRemap(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantStr  string
		wantCols []int // colMap[0..len(input)] — visual column of each rune, plus final width
	}{
		{
			name:     "no tabs",
			input:    "abc",
			wantStr:  "abc",
			wantCols: []int{0, 1, 2, 3},
		},
		{
			name:     "single tab at column 0",
			input:    "\t",
			wantStr:  "    ", // tabWidth=4, at col 0 → 4 spaces
			wantCols: []int{0, 4},
		},
		{
			name:     "tab at column 2",
			input:    "ab\t",
			wantStr:  "ab  ", // at col 2 → 2 spaces to reach col 4
			wantCols: []int{0, 1, 2, 4},
		},
		{
			name:     "tab at column 4",
			input:    "abcd\t",
			wantStr:  "abcd    ", // at col 4 → 4 spaces to reach col 8
			wantCols: []int{0, 1, 2, 3, 4, 8},
		},
		{
			name:     "two tabs",
			input:    "\t\t",
			wantStr:  "        ", // 4 + 4 spaces
			wantCols: []int{0, 4, 8},
		},
		{
			name:     "mixed",
			input:    "a\tb",
			wantStr:  "a   b", // 'a' at 0, tab fills to col 4 (3 spaces), 'b' at 4
			wantCols: []int{0, 1, 4, 5},
		},
		{
			name:     "empty",
			input:    "",
			wantStr:  "",
			wantCols: []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expanded, colMap := expandTabsRemap([]rune(tt.input))
			if got := string(expanded); got != tt.wantStr {
				t.Errorf("expanded = %q, want %q", got, tt.wantStr)
			}
			if len(colMap) != len(tt.wantCols) {
				t.Fatalf("colMap len = %d, want %d", len(colMap), len(tt.wantCols))
			}
			for i, want := range tt.wantCols {
				if colMap[i] != want {
					t.Errorf("colMap[%d] = %d, want %d", i, colMap[i], want)
				}
			}
		})
	}
}
