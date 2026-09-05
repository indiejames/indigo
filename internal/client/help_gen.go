package client

import (
	"sort"
	"strings"
)

// categoryOrder is the fixed display order for the generated help sections,
// a rough basic-to-advanced narrative: move around, then jump around, then
// edit text (with Insert mode's own keys right after, since Editing's
// i/a/A/o/O is how you get there) — then selection and its Match extension,
// then search, then multi-cursor work, then two specific editing
// transforms (sort/case), then power-user features (marks/macros), then
// language-server features, then file/buffer management, then the two
// catch-all menus. "Insert mode" is a synthetic category (its rows come
// from insertCmds, not prefixCmds — see generateHelpEntries) that
// participates in this same ordering mechanism rather than being a
// separately-appended section, specifically so it can be positioned
// anywhere in the sequence instead of always trailing at the very end.
// A category not listed here — e.g. a future user-defined [[keymenu]] — is
// appended after, sorted alphabetically, rather than dropped: this is what
// makes a user-defined menu discoverable in the popup automatically, with
// no changes needed here when phase 4 lands.
var categoryOrder = []string{
	"Navigation",
	"Go to (g)",
	"Editing",
	"Insert mode",
	"Selection",
	"Match (m)",
	"Search",
	"Multi-cursor",
	"Sort (s)",
	"Case (~)",
	"Marks & Macros",
	"LSP / Diagnostics",
	"Files & Buffers",
	"Command (space)",
	"System",
}

// treeHelpRow is one merged-by-name row collected while walking a command
// tree: every occurrence of name, plus that action's canonical
// label/category (see canonicalDisplayInfo's "first occurrence wins" note
// — TestNoDuplicateActionLabels guarantees every occurrence agrees anyway).
//
// rawKeys holds each occurrence's own leaf key alone (e.g. "w") — a valid
// single key a [[keybind]] config could use, for DumpKeybindsTOML's
// placeholder. seqs holds the full, human-readable key SEQUENCE from the
// tree's root down to that occurrence (e.g. "miw" for m→i→w, "gd" for
// g→d, plain "w" for a root-level leaf) — this is what must be shown to a
// human, since a bare leaf key is ambiguous or actively misleading once
// nested more than one level deep: Match's "i" (inside) and "a" (around)
// submenus reuse the exact same leaf letters for every entry, so showing
// just "w" for both would make two different actions render as identical
// rows.
type treeHelpRow struct {
	name     string
	rawKeys  []string
	seqs     []string
	label    string
	category string
}

// collectHelpRows walks cmds (recursing into children, inheriting each
// node's category down to descendants that don't set their own, and
// tracking the full key path down to each leaf) and returns one row per
// distinct action name, in first-encountered order, with every occurrence
// collected onto it.
func collectHelpRows(cmds []command) []*treeHelpRow {
	rows := make(map[string]*treeHelpRow)
	var order []string
	var walk func([]command, string, []string)
	walk = func(cs []command, inherited string, path []string) {
		for _, c := range cs {
			cat := inherited
			if c.category != "" {
				cat = c.category
			}
			fullPath := append(append([]string{}, path...), c.key)
			if c.execute != nil && c.name != "" {
				row, ok := rows[c.name]
				if !ok {
					row = &treeHelpRow{name: c.name, label: c.label, category: cat}
					rows[c.name] = row
					order = append(order, c.name)
				}
				row.rawKeys = append(row.rawKeys, c.key)
				row.seqs = append(row.seqs, displaySequence(fullPath))
			}
			if len(c.children) > 0 {
				walk(c.children, cat, fullPath)
			}
		}
	}
	walk(cmds, "", nil)
	out := make([]*treeHelpRow, len(order))
	for i, name := range order {
		out[i] = rows[name]
	}
	return out
}

// orderedCategories returns byCategory's keys following categoryOrder, with
// any category not listed there (e.g. a future user-defined menu) appended
// after, sorted alphabetically for determinism.
func orderedCategories(byCategory map[string][]*treeHelpRow) []string {
	var cats []string
	seen := map[string]bool{}
	for _, c := range categoryOrder {
		if _, ok := byCategory[c]; ok {
			cats = append(cats, c)
			seen[c] = true
		}
	}
	var extra []string
	for c := range byCategory {
		if !seen[c] {
			extra = append(extra, c)
		}
	}
	sort.Strings(extra)
	return append(cats, extra...)
}

// specialKeyNames renders a handful of raw tree key spellings the way the
// old hand-written help text did (arrows, named keys, the Space-menu
// prefix). Anything not listed here — including every plain single-rune
// key like "p" or "%" — is shown as-is.
var specialKeyNames = map[string]string{
	"left": "←", "right": "→", "up": "↑", "down": "↓",
	"home": "Home", "end": "End", "pgup": "PgUp", "pgdown": "PgDn",
	"esc": "Esc", "tab": "Tab", "enter": "Enter", "backspace": "Backspace",
	"delete": "Delete", "space": "Space", " ": "Space",
}

// displayKey renders a single raw tree key string for human display: known
// special names/arrows get substituted, and a "ctrl+"/"shift+"/"alt+"
// modifier prefix gets capitalized (so "ctrl+p" reads "Ctrl+p", matching
// the old hand-written convention) without touching the key itself. Never
// use this for a [[keybind]] config value — that needs the raw key string
// itself (treeHelpRow.rawKeys), not this cosmetic form.
func displayKey(key string) string {
	if name, ok := specialKeyNames[key]; ok {
		return name
	}
	parts := strings.Split(key, "+")
	for i, p := range parts {
		if name, ok := specialKeyNames[p]; ok {
			parts[i] = name
		} else if p == "ctrl" || p == "shift" || p == "alt" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

// displaySequence renders a full key path (root to leaf) for human
// display. A single-key path gets full cosmetic treatment via displayKey
// (arrows, Home/PgDn/etc., Ctrl+/Shift+/Alt+ capitalization) exactly as a
// root-level binding always has. A multi-key path (reaching into a menu)
// is rendered by concatenating each key directly with no separator,
// matching this codebase's existing two-level convention (e.g. "gd", "~s")
// extended naturally to deeper paths (e.g. "miw") — except the Space-menu
// prefix, which reads as "SPC " (matching the existing "SPC a" convention)
// since a bare space concatenated with the next key would be invisible.
func displaySequence(path []string) string {
	if len(path) == 1 {
		return displayKey(path[0])
	}
	var b strings.Builder
	for _, tok := range path {
		if tok == "space" {
			b.WriteString("SPC ")
			continue
		}
		b.WriteString(tok)
	}
	return b.String()
}

// generateHelpEntries builds the ? popup's built-in reference from the live
// prefixCmds/insertCmds trees and the ":" command table (completion.go's
// generateExCommands), replacing the old hand-maintained static helpEntries
// list. It reflects [[keybind]] overrides applied via applyKeybindOverrides
// (prefixCmds/insertCmds are the live, post-override trees) — a rebound
// key never shows a stale description, and a new ":" command or Insert-mode
// binding can never drift out of sync since there's only one place either
// is declared.
func generateHelpEntries() []helpEntry {
	var out []helpEntry

	byCategory := map[string][]*treeHelpRow{}
	for _, row := range collectHelpRows(prefixCmds) {
		byCategory[row.category] = append(byCategory[row.category], row)
	}
	// "Insert mode" is synthetic — its rows come from a different tree
	// (insertCmds) entirely — but folding it into the same map lets it
	// participate in categoryOrder's positioning like any other section,
	// instead of being hardcoded to always trail at the very end.
	//
	// Exclude rows whose own canonical category is "System" first: since
	// exOnlyActions are merged into both modes' [[keybind]] action sets
	// (keybinds.go), a user can bind an ex-only System action (e.g.
	// "toggle-metrics") into insert mode, and rebindRoot correctly stamps
	// that node's category as "System" via canonicalDisplayInfo — but
	// unlike the Normal-mode categories below, rows here were being folded
	// into "Insert mode" unconditionally, ignoring that category entirely.
	// That let a System action bound into insert mode leak into the popup
	// even though the same action is deliberately hidden when reached from
	// Normal mode.
	if insertRows := collectHelpRows(insertCmds); len(insertRows) > 0 {
		var visible []*treeHelpRow
		for _, row := range insertRows {
			if row.category != "System" {
				visible = append(visible, row)
			}
		}
		if len(visible) > 0 {
			byCategory["Insert mode"] = visible
		}
	}
	// "System" (quit-hint, command-mode, show-plugin-bindings) is
	// meta/self-referential or already a universally-known convention (":"),
	// not worth a curated reference entry — excluded from the popup only.
	// DumpKeybindsTOML shares this same categorization but doesn't apply
	// this exclusion, since these are still legitimate [[keybind]] targets.
	delete(byCategory, "System")

	for _, cat := range orderedCategories(byCategory) {
		out = append(out, helpEntry{key: cat})
		for _, row := range byCategory[cat] {
			out = append(out, helpEntry{key: strings.Join(row.seqs, " / "), desc: row.label})
		}
		out = append(out, helpEntry{key: ""})
	}

	out = append(out, helpEntry{key: "Commands (:)"})
	for _, c := range generateExCommands() {
		out = append(out, helpEntry{key: "  " + c.displayName(), desc: c.desc})
	}

	return out
}
