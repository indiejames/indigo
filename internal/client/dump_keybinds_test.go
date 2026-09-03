package client

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/indiejames/indigo/internal/config"
)

// TestDumpKeybindsTOMLContainsKnownActions is a basic content smoke check:
// built-in actions, section headers, and the ex-only/Insert-mode sections
// are all present.
func TestDumpKeybindsTOMLContainsKnownActions(t *testing.T) {
	out := DumpKeybindsTOML()
	for _, want := range []string{
		`action = "save"`,
		`action = "cursor-left"`,
		`action = "quit-force"`,
		"# --- Navigation (normal mode) ---",
		"# --- Insert mode ---",
		"# --- No default key (bind one to use it as a keypress) ---",
		// "System" is excluded from the ? popup (a curated reference) but
		// stays in the dump (still legitimate [[keybind]] targets).
		"# --- System (normal mode) ---",
		`action = "show-plugin-bindings"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dump output missing %q", want)
		}
	}
}

// TestDumpKeybindsTOMLUncommentedBlockParses proves an uncommented block
// from the dump is valid [[keybind]] TOML a user could actually paste and
// use — not just well-formatted comment text.
func TestDumpKeybindsTOMLUncommentedBlockParses(t *testing.T) {
	out := DumpKeybindsTOML()
	marker := `action = "cursor-left"`
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("dump missing %q", marker)
	}
	start := strings.LastIndex(out[:idx], "# [[keybind]]")
	if start < 0 {
		t.Fatal("could not find the start of the cursor-left block")
	}
	block := out[start : idx+len(marker)]

	var uncommented strings.Builder
	for _, line := range strings.Split(block, "\n") {
		uncommented.WriteString(strings.TrimPrefix(line, "# "))
		uncommented.WriteString("\n")
	}

	var cfg config.Config
	if _, err := toml.Decode(uncommented.String(), &cfg); err != nil {
		t.Fatalf("uncommented block failed to parse as TOML: %v\nblock:\n%s", err, uncommented.String())
	}
	if len(cfg.Keybinds) != 1 {
		t.Fatalf("expected exactly one [[keybind]] entry, got %d: %+v", len(cfg.Keybinds), cfg.Keybinds)
	}
	kb := cfg.Keybinds[0]
	if kb.Mode != "normal" || kb.Key != "left" || kb.Action != "cursor-left" {
		t.Errorf("parsed keybind = %+v, want {Mode:normal Key:left Action:cursor-left}", kb)
	}

	// And it must actually be accepted as a real override, closing the loop
	// from "the dump says this is a valid action" to "applyKeybindOverrides
	// agrees."
	resetKeybinds(t)
	if warnings := applyKeybindOverrides(&cfg); len(warnings) != 0 {
		t.Errorf("applyKeybindOverrides rejected the dump's own output: %v", warnings)
	}
}
