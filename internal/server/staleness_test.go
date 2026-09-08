package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// TestStaleWatchConcurrentAccess exercises watch and changed from many
// goroutines at once. Connect both writes (registering plugin binaries) and
// reads (staleDescription) this map, and the server handles each client
// connection on its own goroutine — so two clients connecting simultaneously
// raced here. For a Go map that is not a torn read but a fatal "concurrent map
// writes", i.e. the whole server dying.
//
// Run with -race to get the full value from this.
func TestStaleWatchConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := range 8 {
		p := filepath.Join(dir, fmt.Sprintf("bin%d", i))
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}

	w := newStaleWatch()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range paths {
				w.watch(p)
				w.watchStamped(p, binaryStamp{size: 1, modTime: 1})
				w.changed()
				w.stale()
				w.staleDescription()
			}
		}()
	}
	wg.Wait()
}

// TestWatchStampedKeepsTheLaunchTimeBaseline covers the reason plugin stamps
// are taken at launch rather than at registration.
//
// Plugins start asynchronously, so they are registered at the first Connect
// that sees them — potentially hours later. Stamping the file at that point
// adopts whatever is on disk *then* as the baseline, so a `make install-plugin`
// done in between is invisible and the running plugin is reported as current
// forever.
func TestWatchStampedKeepsTheLaunchTimeBaseline(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "plugin")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	launch, ok := stampOf(bin)
	if !ok {
		t.Fatal("could not stamp the binary")
	}

	// The binary is replaced before anything registers it — the plugin process
	// is still running v1.
	if err := os.WriteFile(bin, []byte("v2-and-longer"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := &staleWatch{paths: map[string]binaryStamp{}}
	w.watchStamped(bin, launch)
	if !w.stale() {
		t.Error("a plugin binary replaced between launch and registration was not reported " +
			"stale; stamping at registration time hides exactly this case")
	}

	// Re-registering on a later Connect must not overwrite the baseline, or the
	// staleness disappears the second time anyone connects.
	w.watch(bin)
	if !w.stale() {
		t.Error("re-registering the path overwrote the launch-time baseline")
	}
}
