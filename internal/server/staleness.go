package server

import (
	"fmt"
	"os"
	"sync"

	"github.com/indiejames/indigo/internal/binstamp"
)

// Staleness detection: is this server process running code that has since
// been replaced on disk?
//
// The server is long-lived by design — one per workspace, alive as long as any
// client is attached — so `make install` (or `make install-<plugin>`) replaces
// a binary while the old code keeps serving. Nothing about that is visible:
// the client connects normally, the feature you just built is absent, and the
// obvious conclusion is that the code is wrong rather than stale. That
// misdiagnosis cost three separate rounds in one session before it was
// recognised.
//
// The server deliberately does not restart itself. Someone may be editing in
// it, and dropping their session to pick up a new build would be a far worse
// failure than serving old code. Instead it reports the condition at connect
// time and lets each client decide what to say — see the serverStale field on
// connect's result in editor.capnp.
//
// A stale *idle* server needs no special handling: the last client
// disconnecting already shuts it down, so the next connection starts fresh.

// staleWatch remembers what the binaries looked like when this process
// started, so a later stat can tell whether they were replaced underneath it.
type staleWatch struct {
	// mu guards paths. Connect is where both the write (watch) and the read
	// (changed) happen, and the server handles each client connection on its
	// own goroutine — so two clients connecting at once race here, which for a
	// Go map is not merely a torn read but a fatal "concurrent map writes".
	mu sync.Mutex
	// paths maps a binary's path to the stamp it had when this process began
	// using it. A binary we could not stat then is not recorded — it can't be
	// compared against anything, and guessing would produce spurious "stale"
	// reports.
	paths map[string]binstamp.Stamp
}

func newStaleWatch() *staleWatch {
	w := &staleWatch{paths: map[string]binstamp.Stamp{}}
	if exe, err := os.Executable(); err == nil {
		if st, ok := binstamp.Of(exe); ok {
			w.paths[exe] = st
		}
	}
	return w
}

// watch adds a binary to track, stamping it as it is now. Only for binaries
// this process starts using at the moment of the call; anything launched
// earlier must use watchStamped, or a replacement made in between is baked in
// as the baseline and never reported.
func (w *staleWatch) watch(path string) {
	if st, ok := binstamp.Of(path); ok {
		w.watchStamped(path, st)
	}
}

// watchStamped adds a binary to track using a stamp taken at some earlier
// moment — for a plugin, the instant its process was launched from that file.
//
// This is the difference between detecting a replaced plugin and missing it.
// Plugins start asynchronously, so they are registered at the first Connect
// that sees them, which can be hours later; stamping the file then would record
// whatever is on disk at that point, including a build installed since the
// running plugin was launched. The comparison has to be against what the
// running process actually came from.
//
// The first stamp for a path wins: later calls are the same binary being
// re-registered on a subsequent Connect, and overwriting would erase the
// baseline the comparison depends on.
func (w *staleWatch) watchStamped(path string, st binstamp.Stamp) {
	if w == nil || path == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, already := w.paths[path]; already {
		return
	}
	w.paths[path] = st
}

// changed returns the paths whose binaries differ from their startup stamp.
// A binary that has since become unstat-able counts as changed: it was
// replaced by something we cannot see, which is exactly the situation worth
// reporting.
func (w *staleWatch) changed() []string {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	// Copy under the lock and stat outside it: binstamp.Of hits the filesystem
	// once per watched binary, which has no business holding a lock that
	// every Connect needs.
	want := make(map[string]binstamp.Stamp, len(w.paths))
	for path, st := range w.paths {
		want[path] = st
	}
	w.mu.Unlock()

	var out []string
	for path, st := range want {
		if binstamp.Replaced(path, st) {
			out = append(out, path)
		}
	}
	return out
}

// stale reports whether any watched binary has been replaced.
func (w *staleWatch) stale() bool { return len(w.changed()) > 0 }

// staleDescription is a human-readable summary for logs.
func (w *staleWatch) staleDescription() string {
	changed := w.changed()
	if len(changed) == 0 {
		return ""
	}
	return fmt.Sprintf("%d binary/binaries replaced since this server started: %v", len(changed), changed)
}
