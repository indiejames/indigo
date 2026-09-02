package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// TestCollectHelpRowsMergesSharedName verifies leaves sharing a name (e.g.
// "cursor-left" bound at both "h" and "left") merge into one row with both
// keys, in tree order — this is what reproduces the old hand-written
// "h / ←" style entries without hand-authoring them.
func TestCollectHelpRowsMergesSharedName(t *testing.T) {
	resetKeybinds(t)
	rows := collectHelpRows(prefixCmds)
	var row *treeHelpRow
	for _, r := range rows {
		if r.label == "Cursor left" {
			row = r
			break
		}
	}
	if row == nil {
		t.Fatal(`no row found for "Cursor left"`)
	}
	if got := row.rawKeys; len(got) != 2 || got[0] != "h" || got[1] != "left" {
		t.Errorf("cursor-left raw keys = %v, want [h left]", got)
	}
	if got := strings.Join(row.seqs, " / "); got != "h / ←" {
		t.Errorf("cursor-left seqs joined = %q, want %q", got, "h / ←")
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
	if insideWord != "miw" {
		t.Errorf("select-inside-word seq = %q, want %q", insideWord, "miw")
	}
	if aroundWord != "maw" {
		t.Errorf("select-around-word seq = %q, want %q", aroundWord, "maw")
	}
}

// TestDisplaySequence spot-checks the multi-key path rendering rules,
// including the two-level convention this codebase already established
// ("gd") extended to three levels, and the Space-menu's "SPC " form.
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
	if got := displaySequence([]string{" ", "a"}); got != "SPC a" {
		t.Errorf(`displaySequence([" " a]) = %q, want %q`, got, "SPC a")
	}
}

// TestGenerateHelpEntriesCategoryOrder pins the exact section order —
// a basic-to-advanced narrative: movement, then editing (with Insert
// mode's own keys right after it, since that's how you get there), then
// selection and its Match extension, then search/multi-cursor, then two
// specific editing transforms, then power-user and language-server
// features, then the catch-all menus. "Insert mode" is a synthetic
// category (see generateHelpEntries) that must be able to sit anywhere in
// this sequence, not just trail at the very end.
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
		"Navigation", "Go to (g)", "Editing", "Insert mode", "Selection",
		"Match (m)", "Search", "Multi-cursor", "Sort (s)", "Case (~)",
		"Marks & Macros", "LSP / Diagnostics", "Files & Buffers",
		"Command (space)", "Commands (:)",
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

// TestGenerateHelpEntriesIncludesInsertModeAndCommands verifies the two
// non-Normal-mode sections (generated from insertCmds and allCmds
// respectively) are present, rather than the old hand-copied text.
func TestGenerateHelpEntriesIncludesInsertModeAndCommands(t *testing.T) {
	resetKeybinds(t)
	entries := generateHelpEntries()

	var sawInsertHeader, sawCommandsHeader, sawSaveCmd bool
	for _, e := range entries {
		switch e.key {
		case "Insert mode":
			sawInsertHeader = true
		case "Commands (:)":
			sawCommandsHeader = true
		case "  save":
			sawSaveCmd = true
		}
	}
	if !sawInsertHeader {
		t.Error(`missing "Insert mode" section header`)
	}
	if !sawCommandsHeader {
		t.Error(`missing "Commands (:)" section header`)
	}
	if !sawSaveCmd {
		t.Error(`missing "save" entry under Commands (:) (from allCmds)`)
	}
}

// TestGenerateHelpEntriesReflectsOverride is the core bug fix this phase
// closes: a [[keybind]] override must show up in the generated popup
// immediately, unlike the old static helpEntries list which could never
// reflect one.
func TestGenerateHelpEntriesReflectsOverride(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "j", Action: "cursor-up"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	entries := generateHelpEntries()
	var cursorDownKeys, cursorUpKeys string
	for _, e := range entries {
		// Stop at "Insert mode": insertCmds has its own separate "cursor-up"/
		// "cursor-down" actions with the same display label, unaffected by a
		// Normal-mode-only override — scanning past this point would let
		// that unrelated entry overwrite the one this test is checking.
		if e.key == "Insert mode" {
			break
		}
		if e.desc == "Cursor down" {
			cursorDownKeys = e.key
		}
		if e.desc == "Cursor up" {
			cursorUpKeys = e.key
		}
	}
	if strings.Contains(cursorDownKeys, "j") {
		t.Errorf("cursor-down keys = %q, should no longer include %q after rebinding it away", cursorDownKeys, "j")
	}
	if !strings.Contains(cursorUpKeys, "j") {
		t.Errorf("cursor-up keys = %q, should now include %q", cursorUpKeys, "j")
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
