package client

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// cmdDesc pairs a command name with its description and whether it takes arguments.
// Must be kept in alphabetical order (filteredCmds relies on it).
type cmdDesc struct {
	name      string
	desc      string
	needsArgs bool // if true, Tab+Enter fills the command line instead of executing immediately
}

var allCmds = []cmdDesc{
	{"diag", "Open workspace diagnostic browser", false},
	{"diagnostics", "Open workspace diagnostic browser", false},
	{"edit", "Open file picker", false},
	{"extract-rename", "Name a just-extracted function/variable", true},
	{"find", "Workspace search (optional pattern)", true},
	{"fmt", "Format current buffer", false},
	{"format", "Format current buffer", false},
	{"grep", "Workspace search (optional pattern)", true},
	{"metrics", "Toggle metrics overlay", false},
	{"move-to-file", "Move function at cursor to another file", true},
	{"quit", "Close buffer (fails if unsaved)", false},
	{"quit!", "Close buffer, discarding changes", false},
	{"quit-all", "Quit all (fails if any unsaved)", false},
	{"quit-all!", "Quit all, discarding changes", false},
	{"rename", "Rename symbol via language server", true},
	{"save", "Save file", false},
	{"set ft=", "Set this buffer's file type (ft=auto to revert)", true},
	{"wqa", "Save all and quit", false},
	{"write", "Save file", false},
	{"write-quit", "Save and close buffer", false},
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

// filteredCmds returns cmdDesc entries that fuzzy-match input, in alphabetical
// order. Returns nil when input looks like a line number.
func filteredCmds(input string) []cmdDesc {
	if len(input) > 0 && input[0] >= '0' && input[0] <= '9' {
		return nil
	}
	var out []cmdDesc
	for _, e := range allCmds {
		if fuzzyMatch(input, e.name) {
			out = append(out, e)
		}
	}
	return out
}

// maxCmdVisible is the maximum number of command items shown at once.
const maxCmdVisible = 8

// renderCmdCompletionPopup builds styled lines for the command completion popup.
//
// selIdx is the 0-based index of the highlighted command (−1 = no selection).
// When an item is selected, a description box is rendered above the list.
// Items are presented in a single-column list; Tab/Shift+Tab cycle the selection.
func renderCmdCompletionPopup(input string, selIdx int, termW int) []string {
	matches := filteredCmds(input)
	if len(matches) == 0 {
		return nil
	}

	// clamp selIdx to valid range
	if selIdx >= len(matches) {
		selIdx = len(matches) - 1
	}

	// Calculate inner width (between the │ borders).
	// Must accommodate: "Commands" title, "▶ <name> " item rows, " <desc> " desc rows.
	maxNameW := 0
	for _, d := range matches {
		if w := len([]rune(d.name)); w > maxNameW {
			maxNameW = w
		}
	}
	maxDescW := 0
	for _, d := range matches {
		if w := len([]rune(d.desc)); w > maxDescW {
			maxDescW = w
		}
	}
	innerW := max(len("Commands"), 2+maxNameW+1, maxDescW+2)
	innerW = min(innerW, termW-2) // leave room for │ borders

	// Scroll window: center the selection.
	visStart := 0
	visEnd := len(matches)
	if len(matches) > maxCmdVisible {
		if selIdx >= 0 {
			visStart = selIdx - maxCmdVisible/2
		}
		visStart = max(0, min(visStart, len(matches)-maxCmdVisible))
		visEnd = visStart + maxCmdVisible
	}

	var lines []string

	// Description box — shown only when an item is selected.
	if selIdx >= 0 {
		sel := matches[selIdx]

		hdr := bdrH + " " + sel.name + " "
		if len([]rune(hdr)) > innerW {
			hdr = string([]rune(hdr)[:innerW])
		}
		dashes := max(0, innerW-len([]rune(hdr)))
		descTop := popupBorderStyle.Render(bdrTL + hdr + strings.Repeat(bdrH, dashes) + bdrTR)
		lines = append(lines, cmdPopupPad(descTop, termW))

		descAvail := innerW - 2
		desc := sel.desc
		if len([]rune(desc)) > descAvail {
			desc = string([]rune(desc)[:max(0, descAvail-1)]) + "…"
		}
		trail := max(0, descAvail-len([]rune(desc)))
		descRow := popupBorderStyle.Render(bdrV) +
			popupTextStyle.Render(" "+desc+strings.Repeat(" ", trail)+" ") +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, cmdPopupPad(descRow, termW))

		descBot := popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
		lines = append(lines, cmdPopupPad(descBot, termW))
	}

	// Commands box — top border.
	cmdTitle := "Commands"
	cmdDashes := max(0, innerW-len(cmdTitle))
	top := popupBorderStyle.Render(bdrTL) +
		popupTextStyle.Render(cmdTitle) +
		popupBorderStyle.Render(strings.Repeat(bdrH, cmdDashes)+bdrTR)
	lines = append(lines, cmdPopupPad(top, termW))

	// "more above" indicator.
	if visStart > 0 {
		text := fmt.Sprintf("  ↑ %d more", visStart)
		padW := max(0, innerW-len([]rune(text)))
		row := popupBorderStyle.Render(bdrV) +
			popupTextStyle.Render(text+strings.Repeat(" ", padW)) +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, cmdPopupPad(row, termW))
	}

	// Item rows.
	for i := visStart; i < visEnd; i++ {
		name := matches[i].name
		padW := max(0, innerW-2-len([]rune(name)))
		if i == selIdx {
			content := "▶ " + name + strings.Repeat(" ", padW)
			row := popupBorderStyle.Render(bdrV) +
				selectionStyle.Render(content) +
				popupBorderStyle.Render(bdrV)
			lines = append(lines, cmdPopupPad(row, termW))
		} else {
			content := "  " + name + strings.Repeat(" ", padW)
			row := popupBorderStyle.Render(bdrV) +
				popupTextStyle.Render(content) +
				popupBorderStyle.Render(bdrV)
			lines = append(lines, cmdPopupPad(row, termW))
		}
	}

	// "more below" indicator.
	if visEnd < len(matches) {
		text := fmt.Sprintf("  ↓ %d more", len(matches)-visEnd)
		padW := max(0, innerW-len([]rune(text)))
		row := popupBorderStyle.Render(bdrV) +
			popupTextStyle.Render(text+strings.Repeat(" ", padW)) +
			popupBorderStyle.Render(bdrV)
		lines = append(lines, cmdPopupPad(row, termW))
	}

	// Bottom border.
	bottom := popupBorderStyle.Render(bdrBL + strings.Repeat(bdrH, innerW) + bdrBR)
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
