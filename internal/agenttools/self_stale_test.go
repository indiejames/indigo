package agenttools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/binstamp"
)

// writeFakeBinary writes a file standing in for this process's executable, so
// a test can "reinstall" it without touching a real binary.
func writeFakeBinary(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "indigo")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func stampedSelfStale(t *testing.T, path string) *selfStale {
	t.Helper()
	want, ok := binstamp.Of(path)
	if !ok {
		t.Fatalf("stamping %s failed", path)
	}
	return &selfStale{path: path, want: want, known: true}
}

func TestSelfStaleFalseWhileBinaryIsUntouched(t *testing.T) {
	path := writeFakeBinary(t, t.TempDir(), "build one")
	s := stampedSelfStale(t, path)

	if s.stale() {
		t.Error("stale() = true for an unmodified binary, want false")
	}
}

// TestSelfStaleDetectsReinstall is the case that went unnoticed for three
// days: `make install` replaces the binary while the MCP process keeps running
// the old image, and every tool call still looks normal.
func TestSelfStaleDetectsReinstall(t *testing.T) {
	dir := t.TempDir()
	path := writeFakeBinary(t, dir, "build one")
	s := stampedSelfStale(t, path)

	// Same size, so this also covers a rebuild that happens to produce an
	// identically-sized binary — modification time is what separates them.
	later := time.Now().Add(time.Second)
	writeFakeBinary(t, dir, "build two")
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	if !s.stale() {
		t.Error("stale() = false after the binary was replaced, want true")
	}
}

// TestSelfStaleDetectsDeletedBinary covers a replacement done by unlink+create
// where the stat lands in between, and an uninstall: a binary that can't be
// stat-ed is not the one this process started from.
func TestSelfStaleDetectsDeletedBinary(t *testing.T) {
	path := writeFakeBinary(t, t.TempDir(), "build one")
	s := stampedSelfStale(t, path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if !s.stale() {
		t.Error("stale() = false after the binary was deleted, want true")
	}
}

// TestSelfStaleWithoutBaselineIsNeverStale: if the executable couldn't be
// stamped at startup there is nothing to compare against, and reporting
// "stale" on every call would be a false alarm on every call.
func TestSelfStaleWithoutBaselineIsNeverStale(t *testing.T) {
	if (&selfStale{}).stale() {
		t.Error("stale() = true with no baseline, want false")
	}
	var nilStale *selfStale
	if nilStale.stale() {
		t.Error("stale() = true on a nil receiver, want false")
	}
}

// TestNewSelfStaleStampsThisProcess checks the constructor wires itself to the
// real executable — the part a hand-built selfStale in the tests above skips.
func TestNewSelfStaleStampsThisProcess(t *testing.T) {
	s := newSelfStale()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	if !s.known {
		t.Fatal("newSelfStale did not stamp the running executable")
	}
	if s.path != exe {
		t.Errorf("path = %q, want %q", s.path, exe)
	}
	if s.stale() {
		t.Error("stale() = true for the test binary, which nothing has replaced")
	}
}
