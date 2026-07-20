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

// cmdNames extracts just the name field from a []cmdDesc for easier comparison.
func cmdNames(descs []cmdDesc) []string {
	out := make([]string, len(descs))
	for i, d := range descs {
		out[i] = d.name
	}
	return out
}

func TestFilteredCmds(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{"edit", "find", "fmt", "format", "grep", "metrics", "move-to-file", "quit", "quit!", "quit-all", "quit-all!", "rename", "save", "wqa", "write", "write-quit"}},
		// "q" is a subsequence of quit*, wqa, write-quit
		{"q", []string{"quit", "quit!", "quit-all", "quit-all!", "wqa", "write-quit"}},
		{"q!", []string{"quit!", "quit-all!"}},
		// "s" is a subsequence of "metrics" (last char) and "save"
		{"s", []string{"metrics", "save"}},
		{"wq", []string{"wqa", "write-quit"}},
		// "m" matches fmt, format, metrics, move-to-file, and rename (m is a subsequence of all five)
		{"m", []string{"fmt", "format", "metrics", "move-to-file", "rename"}},
		{"z", nil},
		{"123", nil},  // line number — no results
		{"0abc", nil}, // starts with digit
	}
	for _, tt := range tests {
		got := cmdNames(filteredCmds(tt.input))
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
		if all[i].name < all[i-1].name {
			t.Errorf("filteredCmds not sorted: %q before %q", all[i-1].name, all[i].name)
		}
	}
}

func TestRenderCmdCompletionPopupNoMatches(t *testing.T) {
	lines := renderCmdCompletionPopup("zzzz", -1, 80)
	if lines != nil {
		t.Errorf("no matches: want nil, got %v", lines)
	}
}

func TestRenderCmdCompletionPopupReturnsLines(t *testing.T) {
	lines := renderCmdCompletionPopup("", -1, 80)
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
	lines := renderCmdCompletionPopup("", -1, termW)
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != termW {
			t.Errorf("line %d width = %d, want %d", i, w, termW)
		}
	}
}

func TestRenderCmdCompletionPopupNarrowTerminal(t *testing.T) {
	// A very narrow terminal should not panic and should produce at least 1 column.
	lines := renderCmdCompletionPopup("q", -1, 10)
	if lines == nil {
		t.Fatal("narrow terminal: expected lines, got nil")
	}
}

func TestRenderCmdCompletionPopupWithSelection(t *testing.T) {
	// With a selection, popup should include a description box above the list.
	lines := renderCmdCompletionPopup("q", 0, 80)
	if len(lines) == 0 {
		t.Fatal("selection: expected lines, got none")
	}
	// With selection, the total line count should be > without selection
	// (description box adds 3 lines).
	noSel := renderCmdCompletionPopup("q", -1, 80)
	if len(lines) <= len(noSel) {
		t.Errorf("with selection: %d lines, without: %d — expected more with selection", len(lines), len(noSel))
	}
}

func TestRenderCmdCompletionPopupSelectionHighlighted(t *testing.T) {
	// The selected item should contain the selection indicator.
	lines := renderCmdCompletionPopup("q", 0, 80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "▶") {
			found = true
			break
		}
	}
	if !found {
		t.Error("selected popup should contain ▶ indicator")
	}
}

func TestRenderCmdCompletionPopupDescriptionBox(t *testing.T) {
	// With selIdx=0 and input "q", first match is "quit".
	// The description box should contain the command name in its header.
	lines := renderCmdCompletionPopup("q", 0, 80)
	found := false
	for _, line := range lines {
		if strings.Contains(line, "quit") {
			found = true
			break
		}
	}
	if !found {
		t.Error("description box should contain selected command name")
	}
}

func TestRenderCmdCompletionPopupPaddedToWidthWithSelection(t *testing.T) {
	const termW = 60
	lines := renderCmdCompletionPopup("q", 1, termW)
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w != termW {
			t.Errorf("line %d width = %d, want %d (with selection)", i, w, termW)
		}
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
