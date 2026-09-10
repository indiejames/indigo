// Package binstamp identifies an executable file cheaply enough to re-check
// on a hot path, so a long-lived process can tell whether the binary it is
// running from has been replaced on disk since it started.
//
// This exists because indigo's long-lived processes all have the same blind
// spot: a running process keeps the code it was loaded with, so `make install`
// changes the file and nothing else. The server reports it at connect time
// (internal/server/staleness.go) and the MCP process reports it per tool call
// (internal/agenttools) — the primitive is shared so the two can't disagree
// about what "replaced" means.
package binstamp

import "os"

// Stamp is size plus modification time, not a hash: this runs where a
// filesystem round trip is already the budget, and the question is "was this
// replaced", not "is this byte-identical".
type Stamp struct {
	Size    int64
	ModTime int64 // UnixNano
}

// Of stamps the file at path. ok is false if it cannot be stat-ed, which
// callers should treat as "no baseline to compare against" rather than as a
// change — guessing produces spurious stale reports.
func Of(path string) (s Stamp, ok bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Stamp{}, false
	}
	return Stamp{Size: fi.Size(), ModTime: fi.ModTime().UnixNano()}, true
}

// Replaced reports whether the file at path now differs from want.
//
// A file that has since become unstat-able counts as replaced: it was swapped
// for something that can't be seen, which is exactly the situation worth
// reporting.
func Replaced(path string, want Stamp) bool {
	got, ok := Of(path)
	return !ok || got != want
}
