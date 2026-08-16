package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	// Point XDG_CONFIG_HOME at a temp dir with no config file.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.LineNumbers {
		t.Error("default LineNumbers should be true")
	}
}

func TestLoadValidFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "indigo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `line_numbers = false`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LineNumbers {
		t.Error("loaded LineNumbers should be false")
	}
}

func TestLoadInvalidToml(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "indigo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil {
		t.Error("Load() with invalid TOML should return error")
	}
}

func TestLoadMissingDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "nonexistent"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with missing dir error = %v", err)
	}
	if !cfg.LineNumbers {
		t.Error("default LineNumbers should be true when config dir missing")
	}
}

func TestEffectiveIndentHardcodedFallback(t *testing.T) {
	cfg := &Config{}
	got := cfg.EffectiveIndent("elm") // no built-in default for this extension
	want := IndentSettings{Style: "tabs", Width: 4}
	if got != want {
		t.Errorf("EffectiveIndent(elm) = %+v, want %+v", got, want)
	}
}

func TestEffectiveIndentBuiltinPerLanguageDefault(t *testing.T) {
	cfg := &Config{}
	got := cfg.EffectiveIndent("py")
	want := IndentSettings{Style: "spaces", Width: 4}
	if got != want {
		t.Errorf("EffectiveIndent(py) = %+v, want %+v", got, want)
	}
}

func TestEffectiveIndentUserGlobalOverridesBuiltinPerLanguage(t *testing.T) {
	cfg := &Config{IndentStyle: "spaces", IndentWidth: 8}
	got := cfg.EffectiveIndent("go") // built-in default is tabs/4
	want := IndentSettings{Style: "spaces", Width: 8}
	if got != want {
		t.Errorf("EffectiveIndent(go) = %+v, want %+v", got, want)
	}
}

func TestEffectiveIndentUserPerLanguageOverridesEverything(t *testing.T) {
	cfg := &Config{
		IndentStyle:     "spaces",
		IndentWidth:     8,
		IndentOverrides: map[string]IndentSettings{"go": {Style: "tabs", Width: 2}},
	}
	got := cfg.EffectiveIndent("go")
	want := IndentSettings{Style: "tabs", Width: 2}
	if got != want {
		t.Errorf("EffectiveIndent(go) = %+v, want %+v", got, want)
	}
}

func TestEffectiveIndentPartialUserOverrideInheritsOtherField(t *testing.T) {
	cfg := &Config{
		IndentOverrides: map[string]IndentSettings{"py": {Width: 2}}, // style unset
	}
	got := cfg.EffectiveIndent("py")
	want := IndentSettings{Style: "spaces", Width: 2} // style inherited from built-in default
	if got != want {
		t.Errorf("EffectiveIndent(py) = %+v, want %+v", got, want)
	}
}

func TestCursorColumnStyleDefaultNoSetting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "indigo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `line_numbers = false`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CursorColumnStyle != "view" {
		t.Error("loaded CursorColumnStyle should default to `view`")
	}
}

func TestCursorColumnStyleBuffer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfgDir := filepath.Join(dir, "indigo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `cursor_column_style = "buffer"`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.CursorColumnStyle != "buffer" {
		t.Error("loaded CursorColumnStyle should be `buffer`")
	}
}
