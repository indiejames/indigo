package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// resetKeybinds restores prefixCmds/insertCmds to their built-in defaults,
// undoing whatever the test applied. Registered via t.Cleanup so a failed
// assertion never leaks overridden globals into a later test.
func resetKeybinds(t *testing.T) {
	t.Cleanup(func() { applyKeybindOverrides(nil) })
}

func TestApplyKeybindOverridesRebindsExistingKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "j", Action: "cursor-up"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 1
	m2, _ := m.handleNormal(fakeKey("j"))
	got := m2.(Model)
	if got.cursor.Line != 0 {
		t.Errorf("j rebound to cursor-up: cursor.Line = %d, want 0", got.cursor.Line)
	}
}

func TestApplyKeybindOverridesAddsNewKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "select-all"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.sel == nil {
		t.Error("ctrl+g bound to select-all: sel should be set")
	}
}

func TestApplyKeybindOverridesUnknownAction(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "does-not-exist"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown action") {
		t.Errorf("warnings = %v, want one containing 'unknown action'", warnings)
	}
}

func TestApplyKeybindOverridesUnknownMode(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "weird", Key: "ctrl+g", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown mode") {
		t.Errorf("warnings = %v, want one containing 'unknown mode'", warnings)
	}
}

func TestApplyKeybindOverridesEmptyKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "empty key") {
		t.Errorf("warnings = %v, want one containing 'empty key'", warnings)
	}
}

func TestApplyKeybindOverridesRefusesPrefixMenuKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "g", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prefix menu") {
		t.Errorf("warnings = %v, want one containing 'prefix menu'", warnings)
	}
	cmd, ok := findCommand([]string{"g"})
	if !ok || len(cmd.children) == 0 {
		t.Error("g menu should be untouched after a refused override")
	}
}

func TestApplyKeybindOverridesRevertsOnReload(t *testing.T) {
	resetKeybinds(t)
	applyKeybindOverrides(&config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "j", Action: "cursor-up"},
	}})
	// Simulate a config reload that drops the override.
	applyKeybindOverrides(&config.Config{})

	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 1
	m2, _ := m.handleNormal(fakeKey("j"))
	got := m2.(Model)
	if got.cursor.Line != 2 {
		t.Errorf("after reload without override: cursor.Line = %d, want 2 (back to cursor-down)", got.cursor.Line)
	}
}

func TestApplyKeybindOverridesInsertMode(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "insert", Key: "ctrl+g", Action: "line-start"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 3
	m2, _ := m.handleInsert(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.cursor.Col != 0 {
		t.Errorf("ctrl+g bound to line-start: cursor.Col = %d, want 0", got.cursor.Col)
	}
}
