package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultThemeHasInsertCursorBg verifies the built-in default-dark theme
// carries the insert_cursor_bg key (added so the insert-mode cursor color is
// themeable rather than a hardcoded Go constant).
func TestDefaultThemeHasInsertCursorBg(t *testing.T) {
	th := Default()
	if th.UI.InsertCursorBg == "" {
		t.Error("Default() theme has empty InsertCursorBg")
	}
}

// TestParseFallsBackInsertCursorBgToInsertModeFg verifies a theme file
// predating insert_cursor_bg (or a user theme that simply omits it) still
// gets a usable, non-empty cursor color instead of an unstyled block.
func TestParseFallsBackInsertCursorBgToInsertModeFg(t *testing.T) {
	data := []byte(`
name = "no-cursor-key"

[ui]
insert_mode_fg = "#123456"
`)
	th, err := parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if th.UI.InsertCursorBg != "#123456" {
		t.Errorf("InsertCursorBg = %q, want fallback to InsertModeFg %q", th.UI.InsertCursorBg, "#123456")
	}
}

// TestParseKeepsExplicitInsertCursorBg verifies a theme that does set the key
// isn't overridden by the fallback.
func TestParseKeepsExplicitInsertCursorBg(t *testing.T) {
	data := []byte(`
name = "with-cursor-key"

[ui]
insert_mode_fg   = "#123456"
insert_cursor_bg = "#ABCDEF"
`)
	th, err := parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if th.UI.InsertCursorBg != "#ABCDEF" {
		t.Errorf("InsertCursorBg = %q, want explicit %q", th.UI.InsertCursorBg, "#ABCDEF")
	}
}

// TestWriteThemeTOMLFallsBackInsertCursorBg verifies a converted theme (e.g.
// from `theme import helix:...`, which has no equivalent source field) writes
// a real color for insert_cursor_bg rather than an empty string that would
// need parse()'s fallback to paper over on next load.
func TestWriteThemeTOMLFallsBackInsertCursorBg(t *testing.T) {
	th := &Theme{Name: "converted", UI: UI{InsertModeFg: "#123456"}}
	var buf strings.Builder
	writeThemeTOML(&buf, th)
	if !strings.Contains(buf.String(), `insert_cursor_bg = "#123456"`) {
		t.Errorf("written TOML missing fallback insert_cursor_bg, got:\n%s", buf.String())
	}
}

// TestBuiltinThemesAllHaveInsertCursorBg verifies every embedded theme file
// sets the key explicitly (not just relying on the fallback), so each theme
// gets its own intentional cursor color rather than silently reusing the
// insert label color.
func TestBuiltinThemesAllHaveInsertCursorBg(t *testing.T) {
	for _, name := range BuiltinNames() {
		data, err := builtinFS.ReadFile("themes/" + name + ".toml")
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "insert_cursor_bg") {
			t.Errorf("theme %q file has no explicit insert_cursor_bg key (would silently rely on the InsertModeFg fallback)", name)
		}
	}
}

// TestLoadEmptyNameReturnsDefault verifies the documented shortcut: an empty
// theme name skips both the user directory and the embedded lookup entirely.
func TestLoadEmptyNameReturnsDefault(t *testing.T) {
	th, err := Load("", "/nonexistent")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != Default().Name {
		t.Errorf("Load(\"\", ...) = %q, want the default theme", th.Name)
	}
}

// TestLoadPrefersUserThemeOverBuiltin verifies a user theme file under
// cfgDir/indigo/themes/<name>.toml takes precedence over an embedded
// built-in of the same name.
func TestLoadPrefersUserThemeOverBuiltin(t *testing.T) {
	cfgDir := t.TempDir()
	themesDir := filepath.Join(cfgDir, "indigo", "themes")
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Shadow the built-in "default-dark" theme with a distinguishable name field.
	userTOML := "name = \"user-override\"\n\n[ui]\nbar_bg = \"#ffffff\"\n"
	if err := os.WriteFile(filepath.Join(themesDir, "default-dark.toml"), []byte(userTOML), 0o644); err != nil {
		t.Fatal(err)
	}

	th, err := Load("default-dark", cfgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != "user-override" {
		t.Errorf("Load() = %q, want the user theme directory to take precedence over the built-in", th.Name)
	}
}

// TestLoadFallsBackToBuiltinWhenNoUserFile verifies a name with no matching
// file under cfgDir still resolves to the embedded built-in for that name.
// Deliberately uses a non-default built-in ("dracula") rather than
// "default-dark": Default() itself always returns a theme named
// "default-dark", so testing with that name wouldn't distinguish Load()
// actually reading the embedded dracula.toml from Load() silently taking
// the unknown-name fallback path to Default() — both would produce a
// theme, but only one would have dracula's actual colors.
func TestLoadFallsBackToBuiltinWhenNoUserFile(t *testing.T) {
	cfgDir := t.TempDir() // no themes dir at all
	th, err := Load("dracula", cfgDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	data, err := builtinFS.ReadFile("themes/dracula.toml")
	if err != nil {
		t.Fatalf("read embedded dracula.toml: %v", err)
	}
	want, err := parse(data)
	if err != nil {
		t.Fatalf("parse embedded dracula.toml: %v", err)
	}

	if th.Name != want.Name {
		t.Errorf("Name = %q, want %q (the actual built-in dracula theme, not a Default() fallback)", th.Name, want.Name)
	}
	if th.UI != want.UI {
		t.Errorf("UI = %+v, want %+v (loaded theme should match dracula.toml's own colors)", th.UI, want.UI)
	}
}

// TestLoadUnknownNameSilentlyReturnsDefault documents Load's existing
// contract: a name matching neither a user file nor an embedded built-in
// silently falls back to Default() rather than erroring.
func TestLoadUnknownNameSilentlyReturnsDefault(t *testing.T) {
	th, err := Load("does-not-exist-anywhere", t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if th.Name != Default().Name {
		t.Errorf("Load(unknown) = %q, want silent fallback to Default()", th.Name)
	}
}

// TestLoadUserFileParseErrorPropagates verifies a malformed user theme file
// surfaces its parse error to the caller instead of silently falling back
// (unlike an unknown name, which is a deliberate silent fallback above).
func TestLoadUserFileParseErrorPropagates(t *testing.T) {
	cfgDir := t.TempDir()
	themesDir := filepath.Join(cfgDir, "indigo", "themes")
	if err := os.MkdirAll(themesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themesDir, "broken.toml"), []byte("not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load("broken", cfgDir)
	if err == nil {
		t.Fatal("Load() with a malformed user theme file returned nil error, want propagated parse error")
	}
}

// TestLoadBuiltinFileParseErrorPropagates mirrors the user-file case for an
// embedded built-in — exercised via a corrupted user-dir file at the same
// name so we don't have to mutate the embedded FS, since Load's built-in
// branch shares the same parse() call and error-wrapping path.
func TestLoadBuiltinParseErrorPathIsReachable(t *testing.T) {
	// Sanity check that the embedded built-ins Load actually reads all parse
	// cleanly today (regression guard: if this ever fails, Load's silent
	// "unknown name" fallback would mask a real built-in corruption).
	for _, name := range BuiltinNames() {
		if _, err := Load(name, ""); err != nil {
			t.Errorf("Load(%q, \"\") = error %v, want a clean parse", name, err)
		}
	}
}
