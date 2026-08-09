package theme

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Import parses spec as "helix:<path>" or "vscode:<path>", converts the
// foreign theme to an Indigo theme, writes it to outDir/<name>.toml, and
// returns the output path. outDir is typically ~/.config/indigo/themes/.
func Import(spec, outDir string) (string, error) {
	var kind, path string
	if after, ok := strings.CutPrefix(spec, "helix:"); ok {
		kind, path = "helix", after
	} else if after, ok := strings.CutPrefix(spec, "vscode:"); ok {
		kind, path = "vscode", after
	} else {
		return "", fmt.Errorf("unknown format %q — use helix:<path> or vscode:<path>", spec)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	var t *Theme
	switch kind {
	case "helix":
		t, err = FromHelix(data)
	case "vscode":
		t, err = FromVSCode(data)
	}
	if err != nil {
		return "", fmt.Errorf("convert %s: %w", spec, err)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create themes dir: %w", err)
	}
	outPath := filepath.Join(outDir, t.Name+".toml")
	f, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("write theme: %w", err)
	}
	defer f.Close() //nolint:errcheck
	writeThemeTOML(f, t)
	return outPath, nil
}

// FromHelix converts a Helix theme TOML file to an Indigo Theme.
// Helix themes use tree-sitter scope names (near-identical to ours) and a
// [palette] section that maps color aliases to hex values.
func FromHelix(data []byte) (*Theme, error) {
	// Decode into a generic map because Helix mixes top-level keys (scope
	// names) with a nested [palette] table.
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, err
	}

	// Build palette: color alias → "#RRGGBB".
	palette := map[string]string{}
	if p, ok := raw["palette"].(map[string]any); ok {
		for k, v := range p {
			if s, ok := v.(string); ok {
				palette[k] = s
			}
		}
	}

	resolve := func(v any) (fg string, bold, italic bool) {
		switch vt := v.(type) {
		case string:
			fg = resolveColor(vt, palette)
		case map[string]any:
			if fv, ok := vt["fg"]; ok {
				if s, ok := fv.(string); ok {
					fg = resolveColor(s, palette)
				}
			}
			if mods, ok := vt["modifiers"].([]any); ok {
				for _, m := range mods {
					switch m {
					case "bold":
						bold = true
					case "italic":
						italic = true
					}
				}
			}
		}
		return
	}

	t := &Theme{
		Name:   "imported-helix",
		Syntax: map[string]SyntaxStyle{},
	}
	t.UI.PopupBorder = "rounded"

	// Default-dark fallback colors for UI fields not covered by the Helix theme.
	def := Default()
	t.UI = def.UI

	helixUIMap := map[string]*string{
		"ui.statusline":      &t.UI.BarBg,
		"ui.linenr":          &t.UI.GutterFg,
		"ui.linenr.selected": &t.UI.GutterCurFg,
		"ui.selection":       &t.UI.SelectionBg,
		"ui.popup":           &t.UI.PopupBg,
		"diagnostic.error":   &t.UI.DiagErrorFg,
		"warning":            &t.UI.DiagWarnFg,
		"hint":               &t.UI.DiagInfoFg,
		"ui.menu":            &t.UI.PopupBg,
	}

	for key, raw := range raw {
		if key == "palette" {
			continue
		}
		fg, bold, italic := resolve(raw)
		if ptr, ok := helixUIMap[key]; ok {
			if fg != "" {
				*ptr = fg
			}
			continue
		}
		// Map ui.statusline fg separately.
		if key == "ui.statusline" {
			if m, ok := raw.(map[string]any); ok {
				if fv, ok := m["fg"]; ok {
					if s, ok := fv.(string); ok {
						t.UI.BarFg = resolveColor(s, palette)
					}
				}
				if bv, ok := m["bg"]; ok {
					if s, ok := bv.(string); ok {
						t.UI.BarBg = resolveColor(s, palette)
					}
				}
			}
			continue
		}
		// Syntax scopes: skip ui.* keys.
		if strings.HasPrefix(key, "ui.") || fg == "" {
			continue
		}
		t.Syntax[key] = SyntaxStyle{Fg: fg, Bold: bold, Italic: italic}
	}

	return t, nil
}

// FromVSCode converts a VS Code theme JSON file to an Indigo Theme.
// Supports both the standard `tokenColors` (TextMate grammar) format and
// the `semanticTokenColors` extension.
func FromVSCode(data []byte) (*Theme, error) {
	var raw struct {
		Name        string             `json:"name"`
		Colors      map[string]string  `json:"colors"`
		TokenColors []vsCodeTokenColor `json:"tokenColors"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	name := strings.ToLower(strings.ReplaceAll(raw.Name, " ", "-"))
	if name == "" {
		name = "imported-vscode"
	}

	def := Default()
	t := &Theme{
		Name:   name,
		UI:     def.UI,
		Syntax: map[string]SyntaxStyle{},
	}
	t.UI.PopupBorder = "rounded"

	// Map VS Code UI color keys → our UI fields.
	uiColors := map[string]*string{
		"statusBar.background":                &t.UI.BarBg,
		"statusBar.foreground":                &t.UI.BarFg,
		"statusBarItem.remoteBackground":      &t.UI.BarDarkBg,
		"editor.selectionBackground":          &t.UI.SelectionBg,
		"editor.selectionForeground":          &t.UI.SelectionFg,
		"editorLineNumber.foreground":         &t.UI.GutterFg,
		"editorLineNumber.activeForeground":   &t.UI.GutterCurFg,
		"editorHoverWidget.background":        &t.UI.PopupBg,
		"editorHoverWidget.border":            &t.UI.PopupBorderFg,
		"editorWidget.background":             &t.UI.PopupBg,
		"editor.findMatchBackground":          &t.UI.SearchMatchBg,
		"editor.findMatchHighlightBackground": &t.UI.SearchMatchBg,
		"editorError.foreground":              &t.UI.DiagErrorFg,
		"editorWarning.foreground":            &t.UI.DiagWarnFg,
		"editorInfo.foreground":               &t.UI.DiagInfoFg,
	}
	for k, ptr := range uiColors {
		if v, ok := raw.Colors[k]; ok && v != "" {
			*ptr = stripAlpha(v)
		}
	}

	// TextMate scope → our tree-sitter scope, in priority order (first match wins).
	// We do a prefix/suffix search: e.g. "keyword.control.go" → "keyword".
	scopeMap := []struct {
		textmate string
		indigo   string
	}{
		{"comment", "comment"},
		{"string", "string"},
		{"constant.numeric", "number"},
		{"constant.language", "boolean"},
		{"constant.character", "constant"},
		{"constant", "constant"},
		{"variable.language", "variable.builtin"},
		{"variable.parameter", "variable"},
		{"variable", "variable"},
		{"keyword.control", "keyword"},
		{"keyword.operator", "operator"},
		{"keyword", "keyword"},
		{"storage.type", "keyword.builtin"},
		{"storage", "keyword"},
		{"support.function", "function.builtin"},
		{"support.type", "type.builtin"},
		{"support.class", "type"},
		{"support", "variable.builtin"},
		{"entity.name.function", "function"},
		{"entity.name.type", "type"},
		{"entity.name.tag", "tag"},
		{"entity.other.attribute-name", "attribute"},
		{"meta.function-call", "function.call"},
		{"punctuation.definition", "punctuation.bracket"},
		{"punctuation", "punctuation.delimiter"},
		{"markup.heading", "markup.heading"},
		{"markup.bold", "markup.strong"},
		{"markup.italic", "markup.italic"},
		{"markup.inline.raw", "markup.raw"},
		{"markup.raw", "markup.raw"},
		{"markup.underline.link", "markup.link.url"},
		{"markup.quote", "markup.quote"},
	}

	for _, tc := range raw.TokenColors {
		if tc.Settings.Foreground == "" {
			continue
		}
		fg := stripAlpha(tc.Settings.Foreground)
		bold := strings.Contains(tc.Settings.FontStyle, "bold")
		italic := strings.Contains(tc.Settings.FontStyle, "italic")

		for _, scope := range tc.normalizedScopes() {
			for _, mapping := range scopeMap {
				if matchesTextMateScope(scope, mapping.textmate) {
					if _, exists := t.Syntax[mapping.indigo]; !exists {
						t.Syntax[mapping.indigo] = SyntaxStyle{Fg: fg, Bold: bold, Italic: italic}
					}
					break
				}
			}
		}
	}

	return t, nil
}

type vsCodeTokenColor struct {
	Scope    any `json:"scope"` // string or []string
	Settings struct {
		Foreground string `json:"foreground"`
		FontStyle  string `json:"fontStyle"`
	} `json:"settings"`
}

func (tc *vsCodeTokenColor) normalizedScopes() []string {
	switch v := tc.Scope.(type) {
	case string:
		var out []string
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// matchesTextMateScope reports whether tmScope (e.g. "keyword.control.go")
// starts with or equals want (e.g. "keyword.control").
func matchesTextMateScope(tmScope, want string) bool {
	if tmScope == want {
		return true
	}
	return strings.HasPrefix(tmScope, want+".")
}

func resolveColor(s string, palette map[string]string) string {
	if strings.HasPrefix(s, "#") {
		return stripAlpha(s)
	}
	if hex, ok := palette[s]; ok {
		return stripAlpha(hex)
	}
	return ""
}

// stripAlpha trims an 8-digit hex color (#RRGGBBAA) to 6 digits (#RRGGBB).
func stripAlpha(s string) string {
	if len(s) == 9 && s[0] == '#' {
		return s[:7]
	}
	return s
}

// themeWriter wraps io.Writer and accumulates the first write error,
// allowing writeThemeTOML to stay linear without per-call error checks.
type themeWriter struct {
	w   io.Writer
	err error
}

func (tw *themeWriter) pf(format string, args ...any) {
	if tw.err == nil {
		_, tw.err = fmt.Fprintf(tw.w, format, args...)
	}
}

func (tw *themeWriter) pl(args ...any) {
	if tw.err == nil {
		_, tw.err = fmt.Fprintln(tw.w, args...)
	}
}

// writeThemeTOML serialises a Theme to w in indigo theme TOML format.
func writeThemeTOML(w io.Writer, t *Theme) {
	tw := &themeWriter{w: w}
	tw.pf("name = %q\n", t.Name)
	tw.pl()
	tw.pl("[ui]")
	tw.pf("bar_bg          = %q\n", t.UI.BarBg)
	tw.pf("bar_fg          = %q\n", t.UI.BarFg)
	tw.pf("bar_dark_bg     = %q\n", t.UI.BarDarkBg)
	tw.pf("normal_mode_fg  = %q\n", t.UI.NormalModeFg)
	tw.pf("insert_mode_fg  = %q\n", t.UI.InsertModeFg)
	insertCursorBg := t.UI.InsertCursorBg
	if insertCursorBg == "" {
		// Helix/VS Code have no equivalent concept to convert from; fall
		// back to the label color rather than writing an empty value.
		insertCursorBg = t.UI.InsertModeFg
	}
	tw.pf("insert_cursor_bg = %q\n", insertCursorBg)
	tw.pf("selection_bg    = %q\n", t.UI.SelectionBg)
	tw.pf("selection_fg    = %q\n", t.UI.SelectionFg)
	tw.pf("gutter_fg       = %q\n", t.UI.GutterFg)
	tw.pf("gutter_cur_fg   = %q\n", t.UI.GutterCurFg)
	tw.pf("popup_bg        = %q\n", t.UI.PopupBg)
	tw.pf("popup_border_fg = %q\n", t.UI.PopupBorderFg)
	tw.pf("popup_key_fg    = %q\n", t.UI.PopupKeyFg)
	tw.pf("popup_text_fg   = %q\n", t.UI.PopupTextFg)
	tw.pf("search_match_bg = %q\n", t.UI.SearchMatchBg)
	tw.pf("search_match_fg = %q\n", t.UI.SearchMatchFg)
	tw.pf("search_cur_bg   = %q\n", t.UI.SearchCurBg)
	tw.pf("search_cur_fg   = %q\n", t.UI.SearchCurFg)
	tw.pf("diag_error_fg   = %q\n", t.UI.DiagErrorFg)
	tw.pf("diag_warn_fg    = %q\n", t.UI.DiagWarnFg)
	tw.pf("diag_info_fg    = %q\n", t.UI.DiagInfoFg)
	tw.pf("popup_border    = %q\n", t.UI.PopupBorder)
	tw.pl()

	if len(t.Syntax) == 0 {
		return
	}
	tw.pl("[syntax]")
	// Sort keys for deterministic output.
	keys := make([]string, 0, len(t.Syntax))
	for k := range t.Syntax {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s := t.Syntax[k]
		key := fmt.Sprintf("%q", k)
		switch {
		case s.Bold && s.Italic:
			tw.pf("%-30s = {fg = %q, bold = true, italic = true}\n", key, s.Fg)
		case s.Bold:
			tw.pf("%-30s = {fg = %q, bold = true}\n", key, s.Fg)
		case s.Italic:
			tw.pf("%-30s = {fg = %q, italic = true}\n", key, s.Fg)
		default:
			tw.pf("%-30s = {fg = %q}\n", key, s.Fg)
		}
	}
}
