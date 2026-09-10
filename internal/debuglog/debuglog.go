// Package debuglog writes indigo's shared diagnostic log — the one file that
// the app, client, server, plugin manager, and every plugin's stderr all
// append to.
//
// The log rotates daily: each write goes to a file named for the current date
// (indigo-plugins-2006-01-02.log), so a long-running process rolls over to a
// new file on its own at midnight without reopening anything. Files that
// haven't been written to for retention (24h) are deleted, which keeps the log
// bounded in time without ever refusing a write — the failure mode this
// replaced was one unbounded indigo-plugins.log that had reached 11GB.
//
// Deletion is by modification time rather than by the date in the name on
// purpose: a child process (a plugin's stderr, the server's stderr) holds an
// open descriptor to whichever file existed when it was spawned and keeps
// writing there for its whole life, possibly past midnight. Its file's mtime
// stays fresh while it is still being written to, so it is never pruned out
// from under it — on Unix that would not lose the writes, but they would
// silently stop appearing in the log directory.
//
// The directory defaults to os.TempDir() and can be overridden with
// INDIGO_LOG_DIR (also how the tests keep out of the real temp dir).
package debuglog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	// namePrefix is shared by the current file and every rotated one, so a
	// single glob finds them all. The legacy un-rotated "indigo-plugins.log"
	// matches too and is pruned along with the rest.
	namePrefix = "indigo-plugins"

	// retention is how long a log file survives after its last write.
	retention = 24 * time.Hour

	// pruneInterval throttles the directory scan: pruning is triggered by
	// logging, which happens on nearly every RPC, and a glob + stat per line
	// would dwarf the cost of the line itself.
	pruneInterval = time.Hour
)

var (
	mu        sync.Mutex
	lastPrune time.Time
)

// Dir returns the directory holding the log files.
func Dir() string {
	if d := os.Getenv("INDIGO_LOG_DIR"); d != "" {
		return d
	}
	return os.TempDir()
}

// Path returns the log file for the current day. It is recomputed per call
// rather than cached, which is what makes rotation happen with no scheduler:
// the first write after midnight simply names a different file.
func Path() string {
	return filepath.Join(Dir(), fmt.Sprintf("%s-%s.log", namePrefix, time.Now().Format("2006-01-02")))
}

// logOpenFlags opens the day's file for appending, refusing to follow a
// symlink at that path.
//
// Without O_NOFOLLOW this is the classic shared-temp-directory hazard: on a
// multi-user machine os.TempDir() is /tmp, the file name is derived from the
// date and so is entirely predictable, and another user can pre-create it as a
// symlink to a file the *logging* user can write. Every log line would then be
// appended to that target. macOS is not exposed (its per-user
// /var/folders/…/T is private), which is exactly why this is worth enforcing
// in code rather than assuming the local platform's temp directory is safe.
//
// Failing closed is the right trade for a diagnostic log: an open that hits a
// squatted symlink returns ELOOP, the line is dropped, and nothing outside the
// log is written to.
const logOpenFlags = os.O_CREATE | os.O_APPEND | os.O_WRONLY | syscall.O_NOFOLLOW

// logFileMode keeps the log readable only by the user running indigo. Its
// contents are buffer text, file paths, and plugin output — no reason to
// publish those to every account on the machine, which 0644 in a shared /tmp
// would do.
const logFileMode = 0600

// Open opens the current day's log file for appending. The caller closes it.
// Used where a real *os.File is needed rather than a line at a time — a child
// process's stderr, which it then holds for its lifetime.
//
// The directory itself is trusted as far as the platform makes it so: the
// default is whatever os.TempDir() resolves to, and an INDIGO_LOG_DIR override
// is set by the user for their own process — validating a path someone chose
// for themselves protects nobody. The file within it is the part an attacker
// can predict and get to first, and that is what logOpenFlags guards.
func Open() (*os.File, error) {
	maybePrune()
	return os.OpenFile(Path(), logOpenFlags, logFileMode)
}

// Write appends one formatted line to the current day's log file, prefixed
// with tag in brackets ("app" → "[app] ..."). An empty tag writes the line
// unprefixed. Errors are deliberately swallowed: this is diagnostic output,
// and a failure to log must never change what the editor does.
func Write(tag, format string, args ...any) {
	f, err := Open()
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	if tag != "" {
		format = "[" + tag + "] " + format
	}
	fmt.Fprintf(f, format+"\n", args...) //nolint:errcheck
}

// maybePrune runs Prune at most once per pruneInterval per process.
func maybePrune() {
	mu.Lock()
	if !lastPrune.IsZero() && time.Since(lastPrune) < pruneInterval {
		mu.Unlock()
		return
	}
	lastPrune = time.Now()
	mu.Unlock()
	Prune()
}

// Prune deletes log files that have not been written to for retention. The
// current day's file is never considered, so a wrong clock can't delete the
// file being written right now.
//
// Every indigo process prunes, so two can race on the same file; a removal
// that loses the race fails with ErrNotExist and is ignored, same as any
// other removal error (a permission problem is not worth reporting from
// inside a logging call).
func Prune() {
	current := Path()
	matches, err := filepath.Glob(filepath.Join(Dir(), namePrefix+"*.log"))
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retention)
	for _, path := range matches {
		if path == current {
			continue
		}
		// Lstat, not Stat: os.Remove deletes the name, never what a symlink
		// points at, so the mtime that decides its fate should be the name's
		// own — not that of some target a symlink was pointed at to make our
		// own file look fresh (or stale).
		info, err := os.Lstat(path)
		if err != nil || info.IsDir() || info.ModTime().After(cutoff) {
			continue
		}
		os.Remove(path) //nolint:errcheck
	}
}
