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
