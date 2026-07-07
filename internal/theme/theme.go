// Package theme loads and resolves editor color themes.
package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

//go:embed themes/*.toml
var builtinFS embed.FS

// SyntaxStyle describes how a single tree-sitter token scope should be rendered.
type SyntaxStyle struct {
	Fg     string `toml:"fg"`
	Bold   bool   `toml:"bold"`
	Italic bool   `toml:"italic"`
}

// UI holds all theme colors and style options for the editor UI.
type UI struct {
	// Status bar
	BarBg        string `toml:"bar_bg"`
	BarFg        string `toml:"bar_fg"`
	BarDarkBg    string `toml:"bar_dark_bg"` // file type + LSP segments

	// Mode label foregrounds (background = BarBg)
	NormalModeFg string `toml:"normal_mode_fg"`
	InsertModeFg string `toml:"insert_mode_fg"`

	// Editor
	SelectionBg string `toml:"selection_bg"`
	SelectionFg string `toml:"selection_fg"`
	GutterFg    string `toml:"gutter_fg"`
	GutterCurFg string `toml:"gutter_cur_fg"`

	// Popups
	PopupBg       string `toml:"popup_bg"`
	PopupBorderFg string `toml:"popup_border_fg"`
	PopupKeyFg    string `toml:"popup_key_fg"`
	PopupTextFg   string `toml:"popup_text_fg"`

	// Search highlights
	SearchMatchBg string `toml:"search_match_bg"`
	SearchMatchFg string `toml:"search_match_fg"`
	SearchCurBg   string `toml:"search_cur_bg"`
	SearchCurFg   string `toml:"search_cur_fg"`

	// Diagnostics
	DiagErrorFg string `toml:"diag_error_fg"`
	DiagWarnFg  string `toml:"diag_warn_fg"`
	DiagInfoFg  string `toml:"diag_info_fg"`

	// Non-color options
	PopupBorder string `toml:"popup_border"` // rounded | square | double | none
}

// Theme is the complete editor theme loaded from a TOML file.
type Theme struct {
	Name   string                 `toml:"name"`
	UI     UI                     `toml:"ui"`
	Syntax map[string]SyntaxStyle `toml:"syntax"`
}

// BorderChars returns the six box-drawing strings [TL, TR, BL, BR, H, V]
// for the configured popup_border style.
func (t *Theme) BorderChars() [6]string {
	switch t.UI.PopupBorder {
	case "square":
		return [6]string{"┌", "┐", "└", "┘", "─", "│"}
	case "double":
		return [6]string{"╔", "╗", "╚", "╝", "═", "║"}
	case "none":
		return [6]string{" ", " ", " ", " ", " ", " "}
	default: // "rounded"
		return [6]string{"╭", "╮", "╰", "╯", "─", "│"}
	}
}

// Load finds and parses the named theme. It first checks the user's
// ~/.config/indigo/themes/ directory (via cfgDir), then falls back to the
// embedded built-ins. An unknown name silently falls back to Default().
func Load(name, cfgDir string) (*Theme, error) {
	if name == "" {
		return Default(), nil
	}

	// User themes directory takes precedence.
	if cfgDir != "" {
		path := filepath.Join(cfgDir, "indigo", "themes", name+".toml")
		if data, err := os.ReadFile(path); err == nil {
			t, err := parse(data)
			if err != nil {
				return nil, fmt.Errorf("theme %q: %w", name, err)
			}
			return t, nil
		}
	}

	// Embedded built-ins.
	data, err := builtinFS.ReadFile("themes/" + name + ".toml")
	if err != nil {
		// Unknown name — silently use default.
		return Default(), nil
	}
	t, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("built-in theme %q: %w", name, err)
	}
	return t, nil
}

// Default returns the built-in default-dark theme.
func Default() *Theme {
	data, _ := builtinFS.ReadFile("themes/default-dark.toml")
	t, _ := parse(data)
	return t
}

// BuiltinNames returns the names of all embedded built-in themes.
func BuiltinNames() []string {
	entries, _ := fs.ReadDir(builtinFS, "themes")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".toml") {
			names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
		}
	}
	return names
}

func parse(data []byte) (*Theme, error) {
	var t Theme
	if _, err := toml.Decode(string(data), &t); err != nil {
		return nil, err
	}
	if t.UI.PopupBorder == "" {
		t.UI.PopupBorder = "rounded"
	}
	if t.Syntax == nil {
		t.Syntax = map[string]SyntaxStyle{}
	}
	return &t, nil
}
