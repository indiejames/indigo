package workspacefs

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// builtInIgnoredDirs are hidden from the picker, recent files and workspace
// grep unless a config says otherwise.
var builtInIgnoredDirs = []string{
	".git", "vendor", "node_modules",
	".svn", ".hg", "__pycache__", ".cache",
}

// ignoredDirs is the effective set of directories to hide, seeded from
// builtInIgnoredDirs so it's always non-empty even if SetIgnoredDirs is
// never called, and rebuilt on each configuration load to fold in
// Config.PickerIgnoreDirs.
//
// Guarded by ignoredDirsMu and reachable only through IgnoredDirs /
// SetIgnoredDirs, never read directly: SetIgnoredDirs runs on the main
// goroutine on every config hot-reload (polled every 2s by watchConfig),
// while readers run on background goroutines — the file picker's scan
// (startPickerFileScan) and both workspace-grep backends. Reading the bare
// variable from those goroutines is a genuine, race-detector-confirmed data
// race, and one that's bitten twice now (the picker path, then both grep
// paths), so the unsafe spelling is removed entirely rather than left
// available for the next reader to trip over.
var (
	ignoredDirsMu  sync.RWMutex
	ignoredDirs    = newIgnoredDirsSet(nil)
	ignoredDirsGen uint64
)

// ignoredDirsGeneration counts changes to the set, so anything caching results
// derived from it can tell that it has moved on. See candidateFileListCache.
func ignoredDirsGeneration() uint64 {
	ignoredDirsMu.RLock()
	defer ignoredDirsMu.RUnlock()
	return ignoredDirsGen
}

// IgnoredDirs returns the current ignore set for reading. The
// returned map must be treated as read-only: SetIgnoredDirs always installs
// a whole new map rather than mutating the existing one, so a snapshot taken
// here stays valid (and unchanging) for as long as the caller holds it, with
// no further synchronization needed.
func IgnoredDirs() map[string]bool {
	ignoredDirsMu.RLock()
	defer ignoredDirsMu.RUnlock()
	return ignoredDirs
}

// newIgnoredDirsSet builds an ignore set from builtInIgnoredDirs plus extra.
func newIgnoredDirsSet(extra []string) map[string]bool {
	set := make(map[string]bool, len(builtInIgnoredDirs)+len(extra))
	for _, name := range builtInIgnoredDirs {
		set[name] = true
	}
	for _, name := range extra {
		if name != "" {
			set[name] = true
		}
	}
	return set
}

// SetIgnoredDirs rebuilds the ignoredDirs set from built-in defaults plus
// additional directory names (from Config.PickerIgnoreDirs) to hide from the
// file picker, recent files, and workspace grep. Removed entries no longer
// persist across configuration reloads.
func SetIgnoredDirs(names []string) {
	set := newIgnoredDirsSet(names)
	ignoredDirsMu.Lock()
	ignoredDirs = set
	ignoredDirsGen++
	ignoredDirsMu.Unlock()
}

// CollectFiles walks root and returns all workspace-relative file paths.
// ignored is a snapshot of the ignoredDirs set, taken by the caller before
// starting this scan (see the caller) rather than read from the
// shared package-level variable here: CollectFiles runs in its own
// goroutine (the tea.Cmd the caller returns), and ignoredDirs can
// be concurrently reassigned by SetIgnoredDirs on the main goroutine (e.g.
// a config hot-reload) — a genuine, race-detector-confirmed data race
// before this parameter was added. SetIgnoredDirs always builds a whole
// new map rather than mutating the existing one in place, so a snapshot
// taken once, before the goroutine starts, is safe to read for the rest of
// this scan with no further synchronization.
//
// The walk stops early, with truncated set, once the paths would exceed
// MaxCollectBytes, or when ctx is done. The list goes back to the window in
// one RPC message, and a workspace with no repository above it can be a whole
// home directory: unbounded, that message once outgrew Cap'n Proto's 64 MiB
// read limit after a four-minute walk, and the client, unable to decode it,
// dropped the connection and quit with "connection to server lost".
//
// A workspace that is the home directory or / (a start with no repository
// above it) is a whole user's files rather than a project's. There the walk
// goes no more than BroadRootDepth directories down, also reported as
// truncated, which keeps the picker useful for the files near the top
// without spending minutes on the rest.
//
// Directories are read collectBatch entries at a time rather than whole, as
// filepath.WalkDir does, so the budget and cancellation are checked as entries
// arrive even inside one enormous directory. They are visited breadth-first,
// so a list cut short by the budget holds the files nearest the root. As with
// WalkDir, a symlink is listed as a file and never followed, and an
// unreadable directory is skipped.
func CollectFiles(ctx context.Context, root string, ignored map[string]bool) (paths []string, truncated bool) {
	maxDepth := -1
	if isBroadRoot(root) {
		maxDepth = BroadRootDepth
	}
	type dir struct {
		rel   string // "" for root
		depth int
	}
	queue := []dir{{}}
	size := 0
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		f, err := os.Open(filepath.Join(root, d.rel))
		if err != nil {
			continue
		}
		for {
			des, err := f.ReadDir(collectBatch)
			for _, de := range des {
				if ctx.Err() != nil {
					f.Close() //nolint:errcheck
					return finishCollect(paths), true
				}
				rel := filepath.Join(d.rel, de.Name())
				if de.IsDir() {
					if ignored[de.Name()] {
						continue
					}
					if maxDepth >= 0 && d.depth+1 > maxDepth {
						truncated = true
						continue
					}
					queue = append(queue, dir{rel: rel, depth: d.depth + 1})
					continue
				}
				size += len(rel) + collectEntryOverhead
				if size > MaxCollectBytes {
					f.Close() //nolint:errcheck
					return finishCollect(paths), true
				}
				paths = append(paths, rel)
			}
			if err != nil { // io.EOF at the end, or a read error part way
				break
			}
		}
		f.Close() //nolint:errcheck
	}
	return finishCollect(paths), truncated
}

func finishCollect(paths []string) []string {
	sort.Strings(paths)
	return paths
}

// collectBatch is how many entries CollectFiles reads from a directory at a
// time.
const collectBatch = 256

// MaxCollectBytes bounds what CollectFiles returns, counted as each path's
// length plus collectEntryOverhead, roughly what it costs in a Cap'n Proto
// list of text (a pointer, a NUL and padding to a word). 16 MiB is far beyond
// any repository's file list and a quarter of the RPC read limit. A var so
// tests can lower it.
var MaxCollectBytes = 16 << 20

const collectEntryOverhead = 16

// BroadRootDepth is how many directories deep CollectFiles goes in a home
// directory or / workspace: 3 lists ~/a/b/c/file and stops above ~/a/b/c/d/.
const BroadRootDepth = 3

// homeDir is os.UserHomeDir, a var so tests can point it at a temp dir.
var homeDir = os.UserHomeDir

// isBroadRoot reports whether root is the home directory or the filesystem
// root, compared with symlinks resolved, since the workspace path is.
func isBroadRoot(root string) bool {
	r := resolved(root)
	if r == string(filepath.Separator) {
		return true
	}
	home, err := homeDir()
	return err == nil && home != "" && r == resolved(home)
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// Entry is one item in a directory listing.
type Entry struct {
	Name  string
	IsDir bool
}

// ListDir returns the entries of one directory, sorted directories-first and
// then by name — the order the picker browses in.
//
// Separate from CollectFiles because browsing one directory and enumerating a
// whole tree are different costs: this is a single readdir and returns
// immediately, which is why the picker can open instantly and fill in its
// fuzzy-search index afterwards.
func ListDir(absDir string) ([]Entry, error) {
	des, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		out = append(out, Entry{Name: de.Name(), IsDir: de.IsDir()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}
