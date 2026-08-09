package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathResolvesSymlinkedDir(t *testing.T) {
	realDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(realDir, "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	want, err := filepath.EvalSymlinks(filepath.Join(realDir, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}

	got := resolvePath(filepath.Join(linkDir, "file.txt"))
	if got != want {
		t.Errorf("resolvePath(%q) = %q, want %q", filepath.Join(linkDir, "file.txt"), got, want)
	}
}

func TestResolvePathFallsBackWhenUnresolvable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := resolvePath(missing); got != missing {
		t.Errorf("resolvePath(%q) = %q, want unchanged path", missing, got)
	}
}
