package workspacefs

import (
	"io/fs"
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
func CollectFiles(root string, ignored map[string]bool) []string {
	var paths []string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if ignored[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		paths = append(paths, rel)
		return nil
	})
	sort.Strings(paths)
	return paths
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
