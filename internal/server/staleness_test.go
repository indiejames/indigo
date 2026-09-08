package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestStaleWatchDetectsReplacedBinary covers the mechanism behind a failure
// that cost three separate investigations in one session: `make install`
// replaces a binary while the long-lived server keeps serving the old code,
// with nothing anywhere to say so.
func TestStaleWatchDetectsReplacedBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-plugin")
	if err := os.WriteFile(bin, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := &staleWatch{paths: map[string]binaryStamp{}}
	w.watch(bin)

	if w.stale() {
		t.Fatal("reported stale before anything changed")
	}

	// Rewrite with different content *and* a different mtime — an install
	// changes both, and the stamp is (size, mtime).
	if err := os.WriteFile(bin, []byte("a replacement of another size"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if !w.stale() {
		t.Error("did not notice the binary being replaced — this is exactly the case that " +
			"makes a freshly built feature look broken rather than absent")
	}
	if desc := w.staleDescription(); desc == "" {
		t.Error("staleDescription() is empty for a stale watch; the log line would say nothing")
	}
}

// TestStaleWatchIgnoresUnstattableAtStartup pins that a binary we could never
// stat is not watched at all, rather than being treated as permanently
// changed — that would report every server as stale forever.
func TestStaleWatchIgnoresUnstattableAtStartup(t *testing.T) {
	w := &staleWatch{paths: map[string]binaryStamp{}}
	w.watch(filepath.Join(t.TempDir(), "does-not-exist"))
	w.watch("")

	if len(w.paths) != 0 {
		t.Errorf("watched %d unstattable path(s), want none", len(w.paths))
	}
	if w.stale() {
		t.Error("an empty watch reports stale")
	}
}

// TestStaleWatchTreatsDeletionAsChanged covers the other direction: a binary
// that vanishes was replaced by something we cannot see, which is still worth
// reporting rather than silently ignoring.
func TestStaleWatchTreatsDeletionAsChanged(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := &staleWatch{paths: map[string]binaryStamp{}}
	w.watch(bin)
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if !w.stale() {
		t.Error("a deleted binary is not reported as changed")
	}
}

// TestStaleWatchWatchesItsOwnExecutable pins that newStaleWatch stamps the
// running binary — the `make install` case, as opposed to the plugin one.
func TestStaleWatchWatchesItsOwnExecutable(t *testing.T) {
	w := newStaleWatch()
	exe, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine own executable")
	}
	if _, ok := w.paths[exe]; !ok {
		t.Errorf("newStaleWatch() does not watch its own executable (%s); `make install` "+
			"would go unnoticed", exe)
	}
}
