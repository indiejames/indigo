package client

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// cmdDesc is one row of the ":" command completion/help table. Several
// literal tokens can be true aliases of the same action (e.g. "s"/"save"/
// "w"/"write" all just invoke "save") — aliases holds every one of them
// (sorted, name included), so they render as a single combined row instead
// of one identical-looking entry per spelling; name is specifically the
// one alias actually filled into the command line / executed on selection
// (see canonicalAlias), never the combined display form.
type cmdDesc struct {
	name      string
	aliases   []string
	desc      string
	needsArgs bool // if true, Tab+Enter fills the command line instead of executing immediately
}

// displayName is what a popup row shows: every alias for this command
// joined together, matching the "seq1 / seq2" convention the generated ?
// popup already uses for a multi-key binding (help_gen.go's displaySequence).
func (d cmdDesc) displayName() string { return strings.Join(d.aliases, " / ") }

// argCmds are the ":" commands with their own bespoke argument parsing in
// executeCommand (keys_command.go) — grep/find's optional pattern+glob,
// rename's new-name argument, and so on. Unlike every other ":" command,
// these aren't declared in exCommandAliases (which is documented as only
// covering "literal, argument-free" tokens), so they can't be derived from
// it and stay a small hand-maintained list here. None of these currently
// have more than one spelling, but aliases is still filled in by
// generateExCommands for a uniform cmdDesc shape.
var argCmds = []cmdDesc{
	{name: "extract-rename", desc: "Name a just-extracted function/variable", needsArgs: true},
	{name: "find", desc: "Workspace search (optional pattern)", needsArgs: true},
	{name: "grep", desc: "Workspace search (optional pattern)", needsArgs: true},
	{name: "move-to-file", desc: "Move function at cursor to another file", needsArgs: true},
	{name: "rename", desc: "Rename symbol via language server", needsArgs: true},
	{name: "set ft=", desc: "Set this buffer's file type (ft=auto to revert)", needsArgs: true},
}

// canonicalAlias picks which of aliases (a group of literal tokens that all
// resolve to actionName, already sorted) is the one actually filled into
// the command line / executed when its row is selected: actionName itself,
// when it happens to also be a valid literal token for it (true for e.g.
// "save", "quit", "format" — these actions were named to double as their
// own command word) — otherwise the longest alias, which in practice is
// always the most descriptive/readable spelling (e.g. "write-quit" over
// "wq"/"x", "quit!" over "q!"). Ties broken by aliases' existing sort
// order for determinism.
func canonicalAlias(actionName string, aliases []string) string {
	for _, a := range aliases {
		if a == actionName {
			return a
		}
	}
	longest := aliases[0]
	for _, a := range aliases[1:] {
		if len(a) > len(longest) {
			longest = a
		}
	}
	return longest
}

// generateExCommands builds the full ":" command table from
// exCommandAliases — the one declared source of truth for every literal,
// argument-free command token (see its doc comment) — grouped by the
// action name each resolves to, plus argCmds for the handful with their
// own argument parsing. Each group's description comes from its action's
// canonical label (defaultPrefixCmds/exOnlyDisplay, the same lookup
// rebindRoot and the generated help popup use), so a new alias or a
// relabeled action is reflected here automatically with no separate list
// to keep in sync — replaces a hand-maintained allCmds that had quietly
// drifted: several short aliases (w, s, q, q!, qa, qa!, wq, x, e, new)
// were never listed at all, let alone grouped with what they alias.
// Recomputed on every call, like generateHelpEntries, so it can never go
// stale relative to the live command tree.
func generateExCommands() []cmdDesc {
	display := mergeDisplayInfo(canonicalDisplayInfo(defaultPrefixCmds), exOnlyDisplay)

	byAction := map[string][]string{}
	for alias, name := range exCommandAliases {
		byAction[name] = append(byAction[name], alias)
	}

	out := make([]cmdDesc, 0, len(byAction)+len(argCmds))
	for name, aliases := range byAction {
		info, ok := display[name]
		if !ok {
			continue // every alias target is registered; defensive only
		}
		sort.Strings(aliases)
		out = append(out, cmdDesc{name: canonicalAlias(name, aliases), aliases: aliases, desc: info.label})
	}
	for _, c := range argCmds {
		c.aliases = []string{c.name}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
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

// filteredCmds returns cmdDesc entries with at least one alias that
// fuzzy-matches input, in alphabetical order. Returns nil when input looks
// like a line number.
func filteredCmds(input string) []cmdDesc {
	if len(input) > 0 && input[0] >= '0' && input[0] <= '9' {
		return nil
	}
	var out []cmdDesc
	for _, e := range generateExCommands() {
		for _, a := range e.aliases {
			if fuzzyMatch(input, a) {
				out = append(out, e)
				break
			}
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
		if w := len([]rune(d.displayName())); w > maxNameW {
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

		hdr := bdrH + " " + sel.displayName() + " "
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
		name := matches[i].displayName()
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
