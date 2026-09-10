package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/debuglog"
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

// TestServerStderrUsesTheDayLog is the normal path: the spawned server's
// stderr is the day's rotated log file.
func TestServerStderrUsesTheDayLog(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	f := serverStderr()
	if f == nil {
		t.Fatal("serverStderr() = nil with a writable log directory")
	}
	defer f.Close() //nolint:errcheck

	if f.Name() != debuglog.Path() {
		t.Errorf("stderr file = %q, want the day's log %q", f.Name(), debuglog.Path())
	}
}

// TestServerStderrFallsBackToDevNull covers an unwritable log directory: the
// server must still start, and must be handed a real descriptor rather than a
// nil ProcAttr.Files entry — which would leave its fd 2 closed, so a later
// open() could land on 2 and collect everything written to stderr.
func TestServerStderrFallsBackToDevNull(t *testing.T) {
	// A regular file where a directory is expected: opening anything beneath
	// it fails with ENOTDIR.
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDIGO_LOG_DIR", notADir)

	f := serverStderr()
	if f == nil {
		t.Fatal("serverStderr() = nil, want /dev/null so no nil descriptor reaches os.StartProcess")
	}
	defer f.Close() //nolint:errcheck

	if f.Name() != os.DevNull {
		t.Errorf("stderr file = %q, want %q", f.Name(), os.DevNull)
	}
	if _, err := f.WriteString("discarded\n"); err != nil {
		t.Errorf("fallback descriptor is not writable: %v", err)
	}
}
