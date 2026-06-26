package client

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// cmdDesc pairs a full-word command name with a short description.
// Must be kept in alphabetical order (filteredCmds relies on it).
type cmdDesc struct {
	name string
	desc string
}

var allCmds = []cmdDesc{
	{"edit",       "Open file picker"},
	{"metrics",    "Toggle metrics overlay"},
	{"quit",       "Close buffer (fails if unsaved)"},
	{"quit!",      "Close buffer, discarding changes"},
	{"quit-all",   "Quit all (fails if any unsaved)"},
	{"quit-all!",  "Quit all, discarding changes"},
	{"save",       "Save file"},
	{"wqa",        "Save all and quit"},
	{"write-quit", "Save and close buffer"},
}

// fuzzyMatch reports whether every rune of pattern appears in s as a subsequence.
func fuzzyMatch(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	pr := []rune(strings.ToLower(pattern))
	pi := 0
	for _, r := range strings.ToLower(s) {
		if r == pr[pi] {
			pi++
			if pi == len(pr) {
				return true
			}
		}
	}
	return false
}

// filteredCmds returns full-word command names that fuzzy-match input,
// in alphabetical order. Returns nil when input looks like a line number.
func filteredCmds(input string) []string {
	if len(input) > 0 && input[0] >= '0' && input[0] <= '9' {
		return nil
	}
	var out []string
	for _, e := range allCmds {
		if fuzzyMatch(input, e.name) {
			out = append(out, e.name)
		}
	}
	return out
}

// renderCmdCompletionPopup builds styled lines for the command completion popup.
// Items are arranged in the minimum-bounding-rectangle layout (fewest rows).
// Each returned line is padded to termW so it replaces the underlying text line.
func renderCmdCompletionPopup(input string, termW int) []string {
	matches := filteredCmds(input)
	if len(matches) == 0 {
		return nil
	}

	maxName := 0
	for _, n := range matches {
		if w := len([]rune(n)); w > maxName {
			maxName = w
		}
	}
	cellW := maxName + 4 // 2-space padding each side

	// Maximum columns that fit within terminal width (leave 2 for │ borders).
	maxCols := max(1, (termW-2)/cellW)
	cols := min(maxCols, len(matches))
	rows := (len(matches) + cols - 1) / cols

	// Reduce columns to the minimum that still achieves the same row count.
	for cols > 1 && (cols-1)*rows >= len(matches) {
		cols--
	}
	// Recalculate rows after possible column reduction.
	rows = (len(matches) + cols - 1) / cols

	innerW := cols * cellW

	title := "Commands"
	dashes := max(0, innerW-len([]rune(title)))
	top := popupBorderStyle.Render("╭") +
		popupTextStyle.Render(title) +
		popupBorderStyle.Render(strings.Repeat("─", dashes)+"╮")

	var lines []string
	lines = append(lines, cmdPopupPad(top, termW))

	for r := 0; r < rows; r++ {
		var sb strings.Builder
		sb.WriteString(popupBorderStyle.Render("│"))
		for c := 0; c < cols; c++ {
			idx := r*cols + c
			if idx < len(matches) {
				name := matches[idx]
				padR := maxName - len([]rune(name))
				cell := "  " + name + strings.Repeat(" ", padR) + "  "
				sb.WriteString(popupTextStyle.Render(cell))
			} else {
				sb.WriteString(popupTextStyle.Render(strings.Repeat(" ", cellW)))
			}
		}
		sb.WriteString(popupBorderStyle.Render("│"))
		lines = append(lines, cmdPopupPad(sb.String(), termW))
	}

	bottom := popupBorderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")
	lines = append(lines, cmdPopupPad(bottom, termW))
	return lines
}

// cmdPopupPad pads a popup line to the full terminal width.
func cmdPopupPad(line string, termW int) string {
	w := lipgloss.Width(line)
	if w >= termW {
		return ansi.Truncate(line, termW, "")
	}
	return line + strings.Repeat(" ", termW-w)
}
