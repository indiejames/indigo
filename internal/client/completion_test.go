package client

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		pattern, s string
		want       bool
	}{
		{"", "anything", true},
		{"q", "quit", true},
		{"q", "quit!", true},
		{"q!", "quit!", true},
		{"wq", "write-quit", true},
		{"writ", "write-quit", true},
		{"mtr", "metrics", true},
		{"save", "save", true},
		{"z", "quit", false},
		{"qz", "quit", false},
		{"QU", "quit", true}, // case-insensitive
		{"WQ", "write-quit", true},
	}
	for _, tt := range tests {
		got := fuzzyMatch(tt.pattern, tt.s)
		if got != tt.want {
			t.Errorf("fuzzyMatch(%q, %q) = %v, want %v", tt.pattern, tt.s, got, tt.want)
		}
	}
}

func TestFilteredCmds(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{"edit", "metrics", "quit", "quit!", "quit-all", "quit-all!", "save", "wqa", "write-quit"}},
		// "q" is a subsequence of quit*, wqa, write-quit
		{"q", []string{"quit", "quit!", "quit-all", "quit-all!", "wqa", "write-quit"}},
		{"q!", []string{"quit!", "quit-all!"}},
		// "s" is a subsequence of "metrics" (last char) and "save"
		{"s", []string{"metrics", "save"}},
		{"wq", []string{"wqa", "write-quit"}},
		{"m", []string{"metrics"}},
		{"z", nil},
		{"123", nil},  // line number — no results
		{"0abc", nil}, // starts with digit
	}
	for _, tt := range tests {
		got := filteredCmds(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("filteredCmds(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("filteredCmds(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestFilteredCmdsAlphabetical(t *testing.T) {
	all := filteredCmds("")
	for i := 1; i < len(all); i++ {
		if all[i] < all[i-1] {
			t.Errorf("filteredCmds not sorted: %q before %q", all[i-1], all[i])
		}
	}
}

func TestRenderCmdCompletionPopupNoMatches(t *testing.T) {
	lines := renderCmdCompletionPopup("zzzz", 80)
	if lines != nil {
		t.Errorf("no matches: want nil, got %v", lines)
	}
}

func TestRenderCmdCompletionPopupReturnsLines(t *testing.T) {
	lines := renderCmdCompletionPopup("", 80)
	if len(lines) == 0 {
		t.Fatal("empty input should produce popup lines")
	}
	// Must have at least top border + 1 row + bottom border.
	if len(lines) < 3 {
		t.Errorf("popup has %d lines, want >= 3", len(lines))
	}
}

func TestRenderCmdCompletionPopupPaddedToWidth(t *testing.T) {
	const termW = 60
	lines := renderCmdCompletionPopup("", termW)
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != termW {
			t.Errorf("line %d width = %d, want %d", i, w, termW)
		}
	}
}

func TestRenderCmdCompletionPopupNarrowTerminal(t *testing.T) {
	// A very narrow terminal should not panic and should produce at least 1 column.
	lines := renderCmdCompletionPopup("q", 10)
	if lines == nil {
		t.Fatal("narrow terminal: expected lines, got nil")
	}
}

func TestCmdPopupPadShortLine(t *testing.T) {
	line := "hello"
	got := cmdPopupPad(line, 20)
	w := lipgloss.Width(got)
	if w != 20 {
		t.Errorf("padded width = %d, want 20", w)
	}
	if !strings.HasPrefix(got, line) {
		t.Errorf("padded line does not start with %q", line)
	}
}

func TestCmdPopupPadExactWidth(t *testing.T) {
	line := strings.Repeat("x", 20)
	got := cmdPopupPad(line, 20)
	if got != line {
		t.Errorf("exact-width line should not be modified")
	}
}

func TestCmdPopupPadTruncatesLong(t *testing.T) {
	line := strings.Repeat("x", 40)
	got := cmdPopupPad(line, 20)
	w := lipgloss.Width(got)
	if w > 20 {
		t.Errorf("truncated width = %d, want <= 20", w)
	}
}
