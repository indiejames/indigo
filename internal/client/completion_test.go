package client

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
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

// TestFilteredCmds checks canonical names (cmdNames), one per group of
// aliases — e.g. "s"/"save"/"w"/"write" are one row (canonical "save"), so
// a pattern matching only one of those aliases (like "s") still surfaces
// the whole group's single row, not four separate rows or none at all.
func TestFilteredCmds(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", []string{"diagnostics", "extract-rename", "find", "format", "grep", "metrics", "move-to-file", "new", "open", "quit", "quit!", "quit-all", "quit-all!", "rename", "save", "save-as", "set ft=", "wqa", "write-quit"}},
		// "q" is a subsequence of the quit* group's aliases and wqa/write-quit's
		{"q", []string{"quit", "quit!", "quit-all", "quit-all!", "wqa", "write-quit"}},
		{"q!", []string{"quit!", "quit-all!"}},
		// "s" is a subsequence of "diagnostics", "metrics" (last char), the
		// save group (via its "s"/"save" aliases), the save-as group (via its
		// "sa" alias), and "set ft="
		{"s", []string{"diagnostics", "metrics", "save", "save-as", "set ft="}},
		// "wq" matches wqa directly and the write-quit group via its "wq" alias
		{"wq", []string{"wqa", "write-quit"}},
		// "m" matches extract-rename ("rena-m-e"), the format group (via "fmt"/"format"), metrics, move-to-file, and rename
		{"m", []string{"extract-rename", "format", "metrics", "move-to-file", "rename"}},
		// "diag" matches the diagnostics group (via its own "diag" alias) — one row, not two
		{"diag", []string{"diagnostics"}},
		// "extract" is a subsequence of only extract-rename
		{"extract", []string{"extract-rename"}},
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

// TestCanonicalAlias covers both branches: the action's own registry name
// used when it's itself a valid literal token ("save" is one of its own
// aliases), and the longest-alias fallback when it isn't ("quit-force"
// itself is never typed — only "q!"/"quit!" are).
func TestCanonicalAlias(t *testing.T) {
	tests := []struct {
		actionName string
		aliases    []string
		want       string
	}{
		{"save", []string{"s", "save", "w", "write"}, "save"},
		{"quit", []string{"q", "quit"}, "quit"},
		{"quit-force", []string{"q!", "quit!"}, "quit!"},
		{"quit-all-force", []string{"qa!", "quit-all!"}, "quit-all!"},
		{"open-file-picker", []string{"e", "edit"}, "edit"},
		{"new-file", []string{"new"}, "new"},
	}
	for _, tt := range tests {
		if got := canonicalAlias(tt.actionName, tt.aliases); got != tt.want {
			t.Errorf("canonicalAlias(%q, %v) = %q, want %q", tt.actionName, tt.aliases, got, tt.want)
		}
	}
}

// TestGenerateExCommandsGroupsAliases is a regression test for combining
// true aliases of the same action into a single row, per user request:
// "s"/"save"/"w"/"write" must appear as exactly one cmdDesc (not four),
// showing every alias, with a single canonical name used for insertion —
// see keys_command.go's handleCommand, which fills sel.name verbatim into
// the command line on Tab+Enter.
func TestGenerateExCommandsGroupsAliases(t *testing.T) {
	var saveRows int
	var found cmdDesc
	for _, c := range generateExCommands() {
		for _, a := range c.aliases {
			if a == "save" {
				saveRows++
				found = c
			}
		}
	}
	if saveRows != 1 {
		t.Fatalf(`"save" alias appeared in %d rows, want exactly 1 (grouped)`, saveRows)
	}
	if found.name != "save" {
		t.Errorf("canonical name = %q, want %q", found.name, "save")
	}
	wantAliases := []string{"s", "save", "w", "write"}
	if len(found.aliases) != len(wantAliases) {
		t.Fatalf("aliases = %v, want %v", found.aliases, wantAliases)
	}
	for i, a := range wantAliases {
		if found.aliases[i] != a {
			t.Errorf("aliases[%d] = %q, want %q (aliases = %v)", i, found.aliases[i], a, found.aliases)
		}
	}
	if found.displayName() != "s / save / w / write" {
		t.Errorf("displayName() = %q, want %q", found.displayName(), "s / save / w / write")
	}
}

// TestGenerateExCommandsIncludesEveryAlias is a regression test for exactly
// the gap this replaced a hand-maintained allCmds for: several short,
// working ":" aliases (w, s, q, q!, qa, qa!, wq, x, e, new) existed in
// exCommandAliases and worked when typed, but were never listed in the
// completion popup because allCmds was a second, separately-maintained
// list that had drifted out of sync. Deriving the list from
// exCommandAliases directly makes that drift structurally impossible: this
// asserts every single alias declared there appears somewhere in the
// generated table's aliases (grouped rows mean an alias isn't necessarily
// a row's own canonical .name — see canonicalAlias), so a future alias
// added to exCommandAliases without a description can never silently go
// missing from completion/help again.
func TestGenerateExCommandsIncludesEveryAlias(t *testing.T) {
	generated := make(map[string]bool)
	for _, c := range generateExCommands() {
		for _, a := range c.aliases {
			generated[a] = true
		}
	}
	for alias := range exCommandAliases {
		if !generated[alias] {
			t.Errorf("exCommandAliases[%q] has no entry in generateExCommands()", alias)
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

// TestRenderCompletionPopupUniformWidth is a regression test for a border
// off-by-one: rows carrying a detail column were rendered one cell wider than
// the border because innerW budgeted a 1-space detail separator while the row
// used two. Every line of the popup must have identical visual width.
func TestRenderCompletionPopupUniformWidth(t *testing.T) {
	items := []ClientCompletion{
		{Label: "greetLoudly", Detail: "./helper", Kind: 3},
		{Label: "x", Detail: "a", Kind: 6},
	}
	lines := renderCompletionPopup(items, 0, 80)
	if len(lines) < 3 {
		t.Fatalf("want top border + rows + bottom border, got %d lines", len(lines))
	}
	want := lipgloss.Width(lines[0])
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != want {
			t.Errorf("line %d width = %d, want %d (border misaligned)", i, w, want)
		}
	}
}
