package server

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewPropagatesConfigLoadError is a regression test: New() used to
// discard config.Load()'s error entirely (cfg, _ := config.Load()), so a
// config.toml with a parse error left the server silently running with
// whatever partial config resulted, no different from a valid one. New()
// already returns (nil, error) for every other setup failure in this
// function (socket dir, listen, recovery dir) — a broken config must fail
// the same way instead of being swallowed.
func TestNewPropagatesConfigLoadError(t *testing.T) {
	workDir := t.TempDir()
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)

	cfgDir := filepath.Join(cfgHome, "indigo")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := New(workDir)
	if err == nil {
		t.Fatal("New() with a broken config.toml should return an error, got nil")
	}
	if srv != nil {
		t.Error("New() returned a non-nil Server alongside the error")
	}

	// The socket New() creates before loading the config must be cleaned
	// up on this failure path, not left behind as a dangling listener.
	if IsRunning(SocketPath(workDir)) {
		t.Error("socket is still accepting connections after New() failed on config load")
	}
}
