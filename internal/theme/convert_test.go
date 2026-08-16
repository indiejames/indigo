package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFromHelixResolvesPaletteAliases(t *testing.T) {
	data := []byte(`
"keyword" = "red"
"comment" = { fg = "grey", modifiers = ["italic"] }

[palette]
red = "#ff0000"
grey = "#808080"
`)
	th, err := FromHelix(data)
	if err != nil {
		t.Fatalf("FromHelix: %v", err)
	}
	if got := th.Syntax["keyword"].Fg; got != "#ff0000" {
		t.Errorf("keyword fg = %q, want #ff0000 (resolved via palette)", got)
	}
	cmt := th.Syntax["comment"]
	if cmt.Fg != "#808080" || !cmt.Italic {
		t.Errorf("comment = %+v, want fg=#808080 italic=true", cmt)
	}
}

func TestFromHelixDirectHexBypassesPalette(t *testing.T) {
	data := []byte(`"string" = "#00ff00"`)
	th, err := FromHelix(data)
	if err != nil {
		t.Fatalf("FromHelix: %v", err)
	}
	if got := th.Syntax["string"].Fg; got != "#00ff00" {
		t.Errorf("string fg = %q, want #00ff00", got)
	}
}

func TestFromHelixMapsUIKeys(t *testing.T) {
	data := []byte(`
"ui.linenr" = "gutter"
"ui.selection" = "sel"

[palette]
gutter = "#111111"
sel = "#222222"
`)
	th, err := FromHelix(data)
	if err != nil {
		t.Fatalf("FromHelix: %v", err)
	}
	if th.UI.GutterFg != "#111111" {
		t.Errorf("GutterFg = %q, want #111111", th.UI.GutterFg)
	}
	if th.UI.SelectionBg != "#222222" {
		t.Errorf("SelectionBg = %q, want #222222", th.UI.SelectionBg)
	}
	// ui.* keys must never leak into Syntax.
	if _, ok := th.Syntax["ui.linenr"]; ok {
		t.Error("ui.linenr leaked into Syntax map")
	}
}

// TestFromHelixMapsStatuslineFgAndBg is a regression test: ui.statusline
// used to also be listed in helixUIMap (mapped to BarBg only, from the
// table's "fg" field), which meant the dedicated fg→BarFg/bg→BarBg handling
// further down in FromHelix was dead code — the loop always took the
// helixUIMap branch and `continue`d before reaching it. BarFg was never set
// from a Helix theme, and BarBg silently got the fg color instead of bg.
func TestFromHelixMapsStatuslineFgAndBg(t *testing.T) {
	data := []byte(`
"ui.statusline" = { fg = "barfg", bg = "barbg" }

[palette]
barfg = "#111111"
barbg = "#222222"
`)
	th, err := FromHelix(data)
	if err != nil {
		t.Fatalf("FromHelix: %v", err)
	}
	if th.UI.BarFg != "#111111" {
		t.Errorf("BarFg = %q, want #111111 (from ui.statusline.fg)", th.UI.BarFg)
	}
	if th.UI.BarBg != "#222222" {
		t.Errorf("BarBg = %q, want #222222 (from ui.statusline.bg)", th.UI.BarBg)
	}
}

func TestFromHelixInvalidTOMLErrors(t *testing.T) {
	if _, err := FromHelix([]byte("not valid toml [[[")); err == nil {
		t.Fatal("expected an error for malformed TOML, got nil")
	}
}

func TestFromVSCodeMapsUIAndStripsAlpha(t *testing.T) {
	data := []byte(`{
		"name": "My Theme",
		"colors": {
			"statusBar.background": "#123456ff",
			"editor.selectionBackground": "#abcdef"
		},
		"tokenColors": []
	}`)
	th, err := FromVSCode(data)
	if err != nil {
		t.Fatalf("FromVSCode: %v", err)
	}
	if th.Name != "my-theme" {
		t.Errorf("Name = %q, want %q", th.Name, "my-theme")
	}
	if th.UI.BarBg != "#123456" {
		t.Errorf("BarBg = %q, want alpha stripped to #123456", th.UI.BarBg)
	}
	if th.UI.SelectionBg != "#abcdef" {
		t.Errorf("SelectionBg = %q, want #abcdef", th.UI.SelectionBg)
	}
}

func TestFromVSCodeEmptyNameFallsBack(t *testing.T) {
	th, err := FromVSCode([]byte(`{"colors":{},"tokenColors":[]}`))
	if err != nil {
		t.Fatalf("FromVSCode: %v", err)
	}
	if th.Name != "imported-vscode" {
		t.Errorf("Name = %q, want fallback %q", th.Name, "imported-vscode")
	}
}

func TestFromVSCodeMapsTokenColorsFirstMatchWins(t *testing.T) {
	data := []byte(`{
		"name": "tc",
		"tokenColors": [
			{"scope": "keyword.control.go", "settings": {"foreground": "#ff0000", "fontStyle": "bold"}},
			{"scope": "keyword", "settings": {"foreground": "#00ff00"}}
		]
	}`)
	th, err := FromVSCode(data)
	if err != nil {
		t.Fatalf("FromVSCode: %v", err)
	}
	kw := th.Syntax["keyword"]
	if kw.Fg != "#ff0000" || !kw.Bold {
		t.Errorf("keyword = %+v, want the first (more specific) scope match #ff0000 bold=true", kw)
	}
}

func TestFromVSCodeScopeAsArray(t *testing.T) {
	data := []byte(`{
		"name": "arr",
		"tokenColors": [
			{"scope": ["comment.line", "comment.block"], "settings": {"foreground": "#777777"}}
		]
	}`)
	th, err := FromVSCode(data)
	if err != nil {
		t.Fatalf("FromVSCode: %v", err)
	}
	if th.Syntax["comment"].Fg != "#777777" {
		t.Errorf("comment fg = %q, want #777777", th.Syntax["comment"].Fg)
	}
}

func TestFromVSCodeInvalidJSONErrors(t *testing.T) {
	if _, err := FromVSCode([]byte("not json")); err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
}

func TestImportHelixWritesFileAndReturnsPath(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "src.toml")
	if err := os.WriteFile(srcPath, []byte(`"keyword" = "#ff0000"`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()

	outPath, err := Import("helix:"+srcPath, outDir)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !strings.HasPrefix(outPath, outDir) {
		t.Errorf("outPath = %q, want under %q", outPath, outDir)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read written theme: %v", err)
	}
	if !strings.Contains(string(data), "#ff0000") {
		t.Errorf("written theme missing converted color, got:\n%s", data)
	}
}

func TestImportVSCodeWritesFile(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "src.json")
	if err := os.WriteFile(srcPath, []byte(`{"name":"vs","colors":{},"tokenColors":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()

	outPath, err := Import("vscode:"+srcPath, outDir)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if filepath.Base(outPath) != "vs.toml" {
		t.Errorf("outPath base = %q, want vs.toml", filepath.Base(outPath))
	}
}

// TestImportRejectsPathTraversalName is a regression test: a theme's "name"
// field is untrusted (it comes straight from the imported file), and used
// unsanitized to build the output filename. A crafted name containing path
// separators/".." must not let Import write outside outDir.
func TestImportRejectsPathTraversalName(t *testing.T) {
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "evil.json")
	evilJSON := `{"name":"../../../../tmp/indigo-import-traversal-poc","colors":{},"tokenColors":[]}`
	if err := os.WriteFile(srcPath, []byte(evilJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()

	_, err := Import("vscode:"+srcPath, outDir)
	if err == nil {
		t.Fatal("expected Import to reject a traversal theme name, got nil error")
	}

	escaped := filepath.Join(outDir, "..", "..", "..", "..", "tmp", "indigo-import-traversal-poc.toml")
	if _, statErr := os.Stat(escaped); statErr == nil {
		os.Remove(escaped) //nolint:errcheck
		t.Fatal("Import wrote a file outside outDir via a path-traversal theme name")
	}
}

func TestImportUnknownFormatErrors(t *testing.T) {
	if _, err := Import("foo:bar", t.TempDir()); err == nil {
		t.Fatal("expected an error for an unrecognized format prefix, got nil")
	}
}

func TestImportMissingFileErrors(t *testing.T) {
	if _, err := Import("helix:/nonexistent/does-not-exist.toml", t.TempDir()); err == nil {
		t.Fatal("expected an error for a missing source file, got nil")
	}
}

func TestStripAlpha(t *testing.T) {
	cases := map[string]string{
		"#12345678": "#123456",
		"#123456":   "#123456",
		"red":       "red",
	}
	for in, want := range cases {
		if got := stripAlpha(in); got != want {
			t.Errorf("stripAlpha(%q) = %q, want %q", in, got, want)
		}
	}
}
