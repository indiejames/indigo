package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// TestCollectHelpRowsMergesSharedName verifies leaves sharing a name (e.g.
// "page-down" bound at both "ctrl+f" and "pgdown") merge into one row with
// both keys, in tree order — this is what reproduces the old hand-written
// "Ctrl+f / PgDn" style entries without hand-authoring them.
func TestCollectHelpRowsMergesSharedName(t *testing.T) {
	resetKeybinds(t)
	rows := collectHelpRows(prefixCmds)
	var row *treeHelpRow
	for _, r := range rows {
		if r.label == "Page down" {
			row = r
			break
		}
	}
	if row == nil {
		t.Fatal(`no row found for "Page down"`)
	}
	if got := row.rawKeys; len(got) != 2 || got[0] != "ctrl+f" || got[1] != "pgdown" {
		t.Errorf("page-down raw keys = %v, want [ctrl+f pgdown]", got)
	}
	if got := strings.Join(row.seqs, " / "); got != "Ctrl+f / PgDn" {
		t.Errorf("page-down seqs joined = %q, want %q", got, "Ctrl+f / PgDn")
	}
}

// TestCollectHelpRowsShowsFullPathForNestedMenus is the regression test for
// the bug reported live: Match's "i" (inside) and "a" (around) submenus
// reuse the exact same leaf letters (w, s, m, ., f, t, a, c) with identical
// label text for each pair — showing just the bare leaf key made two
// different actions ("select-inside-word" and "select-around-word") render
// as identical-looking rows ("w — Word" twice). The full path must
// distinguish them.
func TestCollectHelpRowsShowsFullPathForNestedMenus(t *testing.T) {
	resetKeybinds(t)
	rows := collectHelpRows(prefixCmds)
	seqFor := func(name string) string {
		for _, r := range rows {
			if r.name == name {
				return strings.Join(r.seqs, " / ")
			}
		}
		t.Fatalf("no row found for action %q", name)
		return ""
	}
	insideWord := seqFor("select-inside-word")
	aroundWord := seqFor("select-around-word")
	if insideWord == aroundWord {
		t.Errorf("select-inside-word and select-around-word render identically (%q) — must be distinguishable", insideWord)
	}
	// The Command menu reaches the Match menu as m.
	if !strings.Contains(insideWord, "Ctrl+Space miw") {
		t.Errorf("select-inside-word seq = %q, want it to include %q", insideWord, "Ctrl+Space miw")
	}
	if !strings.Contains(aroundWord, "Ctrl+Space maw") {
		t.Errorf("select-around-word seq = %q, want it to include %q", aroundWord, "Ctrl+Space maw")
	}
}

// TestDisplaySequence spot-checks the multi-key path rendering rules,
// including the two-level convention this codebase already established
// ("gd") extended to three levels, and the Command menu's "Ctrl+Space " form.
func TestDisplaySequence(t *testing.T) {
	cases := map[string]string{}
	cases["single"] = displaySequence([]string{"left"})
	if cases["single"] != "←" {
		t.Errorf("displaySequence([left]) = %q, want %q", cases["single"], "←")
	}
	if got := displaySequence([]string{"g", "d"}); got != "gd" {
		t.Errorf("displaySequence([g d]) = %q, want %q", got, "gd")
	}
	if got := displaySequence([]string{"m", "i", "w"}); got != "miw" {
		t.Errorf("displaySequence([m i w]) = %q, want %q", got, "miw")
	}
	if got := displaySequence([]string{"space", "a"}); got != "Ctrl+Space a" {
		t.Errorf(`displaySequence([space a]) = %q, want %q`, got, "Ctrl+Space a")
	}
}

// TestGenerateHelpEntriesCategoryOrder pins the exact section order — a
// basic-to-advanced narrative: movement, then editing, then selection and
// its Match extension, then search, then case, then language-server
// features, then the catch-all menus and the palette's commands.
func TestGenerateHelpEntriesCategoryOrder(t *testing.T) {
	resetKeybinds(t)
	entries := generateHelpEntries()

	var headers []string
	for _, e := range entries {
		if e.desc == "" && e.key != "" {
			headers = append(headers, e.key)
		}
	}
	want := []string{
		"Navigation", "Go to (g)", "Editing", "Selection", "Match (m)",
		"Search", "Case (~)", "LSP / Diagnostics", "Debug", "Files & Buffers",
		"Commands", "Command (space)", "Commands (Ctrl+Shift+P)",
	}
	if len(headers) != len(want) {
		t.Fatalf("section headers = %v, want %v", headers, want)
	}
	for i := range want {
		if headers[i] != want[i] {
			t.Errorf("header[%d] = %q, want %q (full: %v)", i, headers[i], want[i], headers)
		}
	}
}

// TestGenerateHelpEntriesIncludesTheKeymapAndCommands verifies the keymap's
// sections and the palette's commands (generated from insertCmds and
// generateExCommands respectively) are present, rather than the old
// hand-copied text.
func TestGenerateHelpEntriesIncludesTheKeymapAndCommands(t *testing.T) {
	resetKeybinds(t)
	entries := generateHelpEntries()

	var sawEditingHeader, sawCommandsHeader, sawSaveCmd bool
	for _, e := range entries {
		switch {
		case e.key == "Editing":
			sawEditingHeader = true
		case e.key == "Commands (Ctrl+Shift+P)":
			sawCommandsHeader = true
		case strings.Contains(e.key, "save") && strings.Contains(e.key, "write"):
			// "save" is grouped with its other aliases (s/save/w/write) into
			// one row — see generateExCommands — so the literal key is the
			// joined "s / save / w / write" form, not a bare "  save". Also
			// requiring "write" (rather than just "save") keeps this from
			// false-matching the separate "sa / save-as" row, which also
			// contains "save" as a substring but never "write".
			sawSaveCmd = true
		}
	}
	if !sawEditingHeader {
		t.Error(`missing "Editing" section header`)
	}
	if !sawCommandsHeader {
		t.Error(`missing "Commands (Ctrl+Shift+P)" section header`)
	}
	if !sawSaveCmd {
		t.Error(`missing "save" entry under Commands (Ctrl+Shift+P) (from generateExCommands)`)
	}
}

// TestGenerateHelpEntriesExcludesSystemFromInsertModeToo is the regression
// test for a gap a review caught: exOnlyActions are valid [[keybind]]
// targets in both modes (keybinds.go), so a System-categorized ex-only
// action (e.g. "toggle-metrics") can be bound into insert mode — and
// rebindRoot correctly stamps that node's category as "System" via
// canonicalDisplayInfo. That must still be excluded from the popup, the
// same as it is when reached from Normal mode, not leak in just because it
// arrived via a different tree. (Note: the reviewer's originally-cited
// example, "show-plugin-bindings" in insert mode, doesn't actually reach
// this path — it's rejected outright as an unknown action for insert mode,
// since normal- and insert-mode action namespaces are separate. This test
// uses "toggle-metrics" instead, confirmed reachable.)
func TestGenerateHelpEntriesExcludesSystemFromInsertModeToo(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "insert", Key: "ctrl+m", Action: "toggle-metrics"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	entries := generateHelpEntries()
	// Scope the check to the "Insert mode" section specifically: "Toggle
	// metrics overlay" is also the legitimate, pre-existing description for
	// the unrelated ":metrics" ex-command under "Commands (:)" (from
	// completion.go's generateExCommands) — checking the whole popup would
	// false-positive on that correct entry.
	inSection := false
	for _, e := range entries {
		if e.desc == "" && e.key != "" {
			inSection = e.key == "Insert mode"
			continue
		}
		if inSection && e.desc == "Toggle metrics overlay" {
			t.Errorf("found %q under Insert mode, want it excluded (System category)", e.desc)
		}
	}
}

// TestGenerateHelpEntriesReflectsOverride is the core bug fix this phase
// closes: a [[keybind]] override must show up in the generated popup
// immediately, unlike the old static helpEntries list which could never
// reflect one.
func TestGenerateHelpEntriesReflectsOverride(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Key: "down", Action: "cursor-up"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	down := displaySequence([]string{"down"})
	entries := generateHelpEntries()
	var cursorDownKeys, cursorUpKeys string
	for _, e := range entries {
		if e.desc == "Cursor down" {
			cursorDownKeys = e.key
		}
		if e.desc == "Cursor up" {
			cursorUpKeys = e.key
		}
	}
	if strings.Contains(cursorDownKeys, down) {
		t.Errorf("cursor-down keys = %q, should no longer include %q after rebinding it away", cursorDownKeys, down)
	}
	if !strings.Contains(cursorUpKeys, down) {
		t.Errorf("cursor-up keys = %q, should now include %q", cursorUpKeys, down)
	}
}

// TestGenerateHelpEntriesExcludesSystemCategory verifies the popup omits
// the "System" section (quit-hint, command-mode, show-plugin-bindings) —
// meta/self-referential entries not worth a curated reference row — while
// leaving the underlying actions themselves untouched.
func TestGenerateHelpEntriesExcludesSystemCategory(t *testing.T) {
	resetKeybinds(t)
	entries := generateHelpEntries()
	for _, e := range entries {
		if e.key == "System" && e.desc == "" {
			t.Error(`"System" section header should not appear in the generated help popup`)
		}
		if e.desc == "Quit hint" || e.desc == "Command mode" {
			t.Errorf("found %q entry in the popup, want it excluded", e.desc)
		}
	}
}

// TestDisplayKey spot-checks the cosmetic key-rendering rules the generator
// relies on for combined entries like "Ctrl+f / PgDn".
func TestDisplayKey(t *testing.T) {
	cases := map[string]string{
		"left":     "←",
		"pgdown":   "PgDn",
		"ctrl+p":   "Ctrl+p",
		"shift+up": "Shift+↑",
		" ":        "Space",
		"%":        "%",
	}
	for key, want := range cases {
		if got := displayKey(key); got != want {
			t.Errorf("displayKey(%q) = %q, want %q", key, got, want)
		}
	}
}
