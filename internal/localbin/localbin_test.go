package localbin

import (
	"os"
	"path/filepath"
	"testing"
)

func writeBin(t *testing.T, dir, cmd string) {
	t.Helper()
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, cmd), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestResolveFindsWorkspaceRootBinary is the baseline, non-monorepo case:
// a single node_modules at the workspace root, file directly under it.
func TestResolveFindsWorkspaceRootBinary(t *testing.T) {
	root := t.TempDir()
	writeBin(t, root, "prettier")

	got, ok := Resolve(root, root, "prettier")
	if !ok {
		t.Fatal("expected to find the workspace-root binary")
	}
	want := filepath.Join(root, "node_modules", ".bin", "prettier")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolveWalksUpFromNestedPackage is the monorepo case this fix
// exists for: a file under a nested package with its own non-hoisted
// node_modules, no binary at the workspace root at all.
func TestResolveWalksUpFromNestedPackage(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "services", "cron-service")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBin(t, pkgDir, "prettier")

	fileDir := filepath.Join(pkgDir, "app", "cronjobs")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := Resolve(fileDir, root, "prettier")
	if !ok {
		t.Fatal("expected to find the nested package's binary by walking up")
	}
	want := filepath.Join(pkgDir, "node_modules", ".bin", "prettier")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestResolvePrefersClosestAncestor verifies that when both a nested
// package and the workspace root have their own binary, the closer
// (nested) one wins — matching real Node module resolution semantics.
func TestResolvePrefersClosestAncestor(t *testing.T) {
	root := t.TempDir()
	writeBin(t, root, "prettier")
	pkgDir := filepath.Join(root, "services", "cron-service")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBin(t, pkgDir, "prettier")

	got, ok := Resolve(pkgDir, root, "prettier")
	if !ok {
		t.Fatal("expected to find a binary")
	}
	want := filepath.Join(pkgDir, "node_modules", ".bin", "prettier")
	if got != want {
		t.Errorf("got %q, want the nested (closer) binary %q, not the workspace root's", got, want)
	}
}

// TestResolveStopsAtWorkspaceRoot verifies the walk doesn't continue past
// workspaceRoot even if a binary happens to exist further up (outside the
// workspace) — it shouldn't matter here since TempDir's ancestry won't
// have one, but this pins the intended boundary explicitly via a case
// with nothing found at all within the workspace.
func TestResolveStopsAtWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	fileDir := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, ok := Resolve(fileDir, root, "prettier")
	if ok {
		t.Fatal("expected no binary to be found anywhere under the workspace")
	}
}

// TestResolveNotFound verifies the plain "nothing installed anywhere"
// case returns false rather than an error.
func TestResolveNotFound(t *testing.T) {
	root := t.TempDir()
	if _, ok := Resolve(root, root, "prettier"); ok {
		t.Fatal("expected not found")
	}
}

// TestResolveStopsAtWorkspaceRootWithTrailingSeparator is a regression test
// for a boundary bug: dir == workspaceRoot is a raw string comparison, and
// filepath.Dir never returns a trailing separator, so an uncleaned
// workspaceRoot with one (e.g. passed straight from a config value or a
// path a caller joined with "/") would never match it — the walk would
// silently continue past the intended workspace root into ancestor
// directories. Places a binary just *outside* the workspace (in root's
// parent) and confirms Resolve, given workspaceRoot with a trailing
// separator, still returns false rather than finding it.
func TestResolveStopsAtWorkspaceRootWithTrailingSeparator(t *testing.T) {
	parent := t.TempDir()
	writeBin(t, parent, "prettier") // outside the workspace

	root := filepath.Join(parent, "workspace")
	fileDir := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(fileDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, ok := Resolve(fileDir, root+string(filepath.Separator), "prettier")
	if ok {
		t.Fatal("expected the walk to stop at workspaceRoot despite its trailing separator, not find the binary just outside it")
	}
}
