package server

import (
	"fmt"
	"os"
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

// binaryStamp identifies a file cheaply enough to check on every connect.
// Size plus modification time, not a hash: this runs on a hot-ish path, and
// the question is "was this replaced", not "is this byte-identical".
type binaryStamp struct {
	size    int64
	modTime int64
}

func stampOf(path string) (binaryStamp, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return binaryStamp{}, false
	}
	return binaryStamp{size: fi.Size(), modTime: fi.ModTime().UnixNano()}, true
}

// staleWatch remembers what the binaries looked like when this process
// started, so a later stat can tell whether they were replaced underneath it.
type staleWatch struct {
	// paths maps a label ("server", or a plugin name) to the stamp its binary
	// had at startup. A binary we could not stat at startup is not recorded —
	// it can't be compared against anything, and guessing would produce
	// spurious "stale" reports.
	paths map[string]binaryStamp
}

func newStaleWatch() *staleWatch {
	w := &staleWatch{paths: map[string]binaryStamp{}}
	if exe, err := os.Executable(); err == nil {
		if st, ok := stampOf(exe); ok {
			w.paths[exe] = st
		}
	}
	return w
}

// watch adds a binary to track, ignoring one that cannot be stat'd.
func (w *staleWatch) watch(path string) {
	if w == nil || path == "" {
		return
	}
	if _, already := w.paths[path]; already {
		return
	}
	if st, ok := stampOf(path); ok {
		w.paths[path] = st
	}
}

// changed returns the paths whose binaries differ from their startup stamp.
// A binary that has since become unstat-able counts as changed: it was
// replaced by something we cannot see, which is exactly the situation worth
// reporting.
func (w *staleWatch) changed() []string {
	if w == nil {
		return nil
	}
	var out []string
	for path, want := range w.paths {
		got, ok := stampOf(path)
		if !ok || got != want {
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
