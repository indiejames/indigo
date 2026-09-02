package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// TestApplyKeymenusSimpleMenu verifies a two-level user-defined menu
// dispatches correctly for each child.
func TestApplyKeymenusSimpleMenu(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Save", Children: []config.KeymenuNode{
			{Key: "s", Action: "save"},
			{Key: "a", Action: "save-as"},
		}},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.prefixSeq == nil {
		t.Fatal("ctrl+g should enter a pending prefix sequence")
	}
	m3, cmd := got.handleNormal(fakeKey("s"))
	if cmd == nil {
		t.Error("ctrl+g s should dispatch save (expected non-nil cmd)")
	}
	_ = m3
}

// TestApplyKeymenusArbitraryDepth verifies a 3-level nested menu dispatches
// correctly — no depth limit, per the design decision.
func TestApplyKeymenusArbitraryDepth(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Outer", Children: []config.KeymenuNode{
			{Key: "i", Label: "Inner", Children: []config.KeymenuNode{
				{Key: "s", Action: "save"},
			}},
		}},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	cmd, ok := findCommand([]string{"ctrl+g", "i", "s"})
	if !ok {
		t.Fatal("ctrl+g i s not found")
	}
	if cmd.name != "save" {
		t.Errorf("name = %q, want %q", cmd.name, "save")
	}
}

// TestApplyKeymenusCollisionWithBuiltinMenu verifies a user-defined menu at
// an existing built-in menu's key (here "s", built-in Sort) fully replaces
// it, with a warning — "config always wins," matching [[keybind]]'s
// existing precedent.
func TestApplyKeymenusCollisionWithBuiltinMenu(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "s", Label: "Custom", Children: []config.KeymenuNode{
			{Key: "x", Action: "save"},
		}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "shadows") {
		t.Fatalf("warnings = %v, want one containing 'shadows'", warnings)
	}
	if _, ok := findCommand([]string{"s", "a"}); ok {
		t.Error("built-in Sort's \"a\" child should be unreachable after shadowing")
	}
	cmd, ok := findCommand([]string{"s", "x"})
	if !ok || cmd.name != "save" {
		t.Error("custom menu's \"x\" child should be reachable at s x")
	}
}

// TestApplyKeymenusCollisionWithBuiltinLeaf verifies the same replace+warn
// behavior for a single-action leaf (ctrl+s, "save"), proving the rule
// isn't menu-specific.
func TestApplyKeymenusCollisionWithBuiltinLeaf(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+s", Label: "Custom", Children: []config.KeymenuNode{
			{Key: "x", Action: "quit"},
		}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "shadows") {
		t.Fatalf("warnings = %v, want one containing 'shadows'", warnings)
	}
	cmd, ok := findCommand([]string{"ctrl+s", "x"})
	if !ok || cmd.name != "quit" {
		t.Error("custom menu's \"x\" child should be reachable at ctrl+s x")
	}
}

// TestApplyKeymenusBothActionAndChildren verifies a node with both an
// action and children is rejected with a descriptive warning, and dropped
// (not partially applied).
func TestApplyKeymenusBothActionAndChildren(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Action: "save", Children: []config.KeymenuNode{
			{Key: "x", Action: "quit"},
		}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "both an action and child") {
		t.Fatalf("warnings = %v, want one containing 'both an action and child'", warnings)
	}
	if _, ok := findCommand([]string{"ctrl+g"}); ok {
		t.Error("invalid top-level node should not be applied at all")
	}
}

// TestApplyKeymenusNeitherActionNorChildren verifies a node with neither is
// rejected.
func TestApplyKeymenusNeitherActionNorChildren(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Empty"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "neither an action nor child") {
		t.Fatalf("warnings = %v, want one containing 'neither an action nor child'", warnings)
	}
}

// TestApplyKeymenusDuplicateSiblingKey verifies a duplicate sibling key
// warns and drops the later entry, while the first sibling (and any other
// valid siblings) still applies.
func TestApplyKeymenusDuplicateSiblingKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Menu", Children: []config.KeymenuNode{
			{Key: "s", Action: "save"},
			{Key: "s", Action: "quit"},
			{Key: "q", Action: "quit"},
		}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "more than one child with key") {
		t.Fatalf("warnings = %v, want one containing 'more than one child with key'", warnings)
	}
	cmd, ok := findCommand([]string{"ctrl+g", "s"})
	if !ok || cmd.name != "save" {
		t.Error("first sibling with key \"s\" (save) should win")
	}
	if _, ok := findCommand([]string{"ctrl+g", "q"}); !ok {
		t.Error("unrelated valid sibling \"q\" should still apply")
	}
}

// TestApplyKeymenusUnknownAction verifies an unknown action reference on a
// leaf warns and drops that leaf without affecting siblings.
func TestApplyKeymenusUnknownAction(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Menu", Children: []config.KeymenuNode{
			{Key: "z", Action: "does-not-exist"},
			{Key: "s", Action: "save"},
		}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown action "does-not-exist"`) {
		t.Fatalf("warnings = %v, want one containing the unknown action", warnings)
	}
	if _, ok := findCommand([]string{"ctrl+g", "z"}); ok {
		t.Error("invalid child (unknown action) should not be reachable at ctrl+g z")
	}
	if _, ok := findCommand([]string{"ctrl+g", "s"}); !ok {
		t.Error("valid sibling should still apply despite the invalid one")
	}
}

// TestApplyKeymenusEmptyTopLevelKey verifies an empty top-level key is
// rejected, mirroring [[keybind]]'s existing empty-key check.
func TestApplyKeymenusEmptyTopLevelKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "", Label: "Nameless", Children: []config.KeymenuNode{{Key: "s", Action: "save"}}},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "empty key") {
		t.Fatalf("warnings = %v, want one containing 'empty key'", warnings)
	}
}

// TestApplyKeymenusRevertsOnReload mirrors
// TestApplyKeybindOverridesRevertsOnReload: a keymenu shadowing a built-in,
// then a reload with no config, must restore the built-in.
func TestApplyKeymenusRevertsOnReload(t *testing.T) {
	resetKeybinds(t)
	applyKeybindOverrides(&config.Config{Keymenus: []config.KeymenuNode{
		{Key: "s", Label: "Custom", Children: []config.KeymenuNode{{Key: "x", Action: "save"}}},
	}})
	applyKeybindOverrides(&config.Config{})

	if _, ok := findCommand([]string{"s", "x"}); ok {
		t.Error("custom menu should be gone after reload without it")
	}
	cmd, ok := findCommand([]string{"s", "a"})
	if !ok || cmd.name != "sort-lines-ascending" {
		t.Error("built-in Sort menu should be restored after reload")
	}
}

// TestApplyKeymenusPopupIntegration is the end-to-end proof that this
// phase's actual dependency on phases 2/3 pays off: a user-defined menu's
// leaves resolve through the same action machinery a built-in menu's
// leaves do, and show up in the generated help popup under their own
// section automatically, with no popup-side changes.
//
// Uses "quit-force" (an ex-only action, never present in the tree until a
// [[keybind]]/[[keymenu]] actually binds it) rather than "save": the
// popup's merge-by-name logic means a leaf referencing an ALREADY-existing
// action (like "save", already rooted at ctrl+s under "Files & Buffers")
// just adds another key to that action's existing row — same category,
// same row, no new section — which is correct (one action, multiple
// access paths, matching how commandMenuRoot's own "p"/"f" children reuse
// g's/root's existing rows rather than creating duplicates). A category
// only gets introduced by an action with no prior tree occurrence.
func TestApplyKeymenusPopupIntegration(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "My Menu", Children: []config.KeymenuNode{
			{Key: "x", Action: "quit-force"},
		}},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	entries := generateHelpEntries()
	var sawHeader, sawChild bool
	for _, e := range entries {
		if e.key == "My Menu" && e.desc == "" {
			sawHeader = true
		}
		if e.desc == "Quit, discarding changes" {
			sawChild = true
		}
	}
	if !sawHeader {
		t.Error(`missing "My Menu" section header in the generated popup`)
	}
	if !sawChild {
		t.Error(`missing "Quit, discarding changes" entry (the custom menu's child) in the generated popup`)
	}
}

// TestApplyKeymenusLabelFallback verifies a leaf with no Label falls back
// to the action's real canonical label, not the bare action name.
func TestApplyKeymenusLabelFallback(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keymenus: []config.KeymenuNode{
		{Key: "ctrl+g", Label: "Menu", Children: []config.KeymenuNode{
			{Key: "s", Action: "save"},
		}},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	cmd, ok := findCommand([]string{"ctrl+g", "s"})
	if !ok {
		t.Fatal("ctrl+g s not found")
	}
	if cmd.label != "Save" {
		t.Errorf("label = %q, want canonical label %q (not bare action name)", cmd.label, "Save")
	}
}
