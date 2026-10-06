package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Opening a path that does not exist must not create it. indigo used to write
// an empty file (and any missing parent directories) at startup, so opening a
// mistyped name and quitting straight back out left litter behind.
func TestResolveTargetDoesNotCreateAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brand-new.txt")

	resolved, isDir, err := resolveTarget(path)
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if isDir {
		t.Error("isDir = true for a path that does not exist")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("%s exists after resolveTarget; it must not be created until save", path)
	}
	if resolved == "" {
		t.Error("resolved path is empty")
	}
	if filepath.Base(resolved) != "brand-new.txt" {
		t.Errorf("resolved = %q, want it to keep the file name", resolved)
	}
}

// Nor may it create the missing directories on the way to that file.
func TestResolveTargetDoesNotCreateMissingDirectories(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	path := filepath.Join(nested, "new.txt")

	if _, _, err := resolveTarget(path); err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); !os.IsNotExist(err) {
		t.Error("intermediate directory was created")
	}
}

// An existing file is reported as a file, an existing directory as a
// directory — the flag the caller uses to decide between opening a buffer and
// opening the file picker.
func TestResolveTargetReportsWhatExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "there.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, isDir, err := resolveTarget(file); err != nil || isDir {
		t.Errorf("file: isDir=%v err=%v, want false/nil", isDir, err)
	}
	if _, isDir, err := resolveTarget(dir); err != nil || !isDir {
		t.Errorf("dir: isDir=%v err=%v, want true/nil", isDir, err)
	}
}

// Symlinks still have to be resolved for a file that does not exist yet: the
// workspace root is derived from this path, and a linter spawned with that
// root as its cwd gets it resolved by the OS either way. EvalSymlinks fails
// outright on a missing path, so the longest existing prefix is resolved and
// the remainder rejoined.
func TestResolveTargetResolvesSymlinksForAMissingFile(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	resolved, _, err := resolveTarget(filepath.Join(link, "sub", "new.txt"))
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	want := filepath.Join(resolvePath(real), "sub", "new.txt")
	if resolved != want {
		t.Errorf("resolved = %q, want %q (symlinked prefix resolved, missing tail kept)", resolved, want)
	}
}

// With no git root, the workspace is the new file's directory — which may not
// exist yet, since opening no longer creates it. The server and the tools it
// spawns run with the workspace as their cwd, so it must fall back to the
// nearest directory that does.
func TestNearestExistingDirWalksUpToAnExistingDirectory(t *testing.T) {
	base := t.TempDir()

	if got := nearestExistingDir(filepath.Join(base, "a", "b")); got != base {
		t.Errorf("missing dirs: got %q, want %q", got, base)
	}
	if got := nearestExistingDir(base); got != base {
		t.Errorf("existing dir: got %q, want it unchanged (%q)", got, base)
	}

	// A file is not a directory; a path under it falls back past it.
	file := filepath.Join(base, "f.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := nearestExistingDir(filepath.Join(file, "x")); got != base {
		t.Errorf("path under a file: got %q, want %q", got, base)
	}
}
