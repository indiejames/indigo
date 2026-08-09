package theme

import (
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
