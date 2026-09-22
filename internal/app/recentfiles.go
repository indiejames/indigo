package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// hostRecentRoot is the workspace as the host names it, when that differs from
// how this process names it — which happens only when attached to a container.
//
// Process-global because it is a property of the process: one indigo window
// serves one workspace. The alternative is an eleventh positional parameter on
// two constructors that are already long, to carry a value that never varies
// within a run.
var hostRecentRoot string

// SetRecentRoot records where the workspace lives on the host, for keying the
// recent-files list. Called once at startup; unset means the workspace is not
// in a container and workDir already is the host path.
func SetRecentRoot(hostRoot string) { hostRecentRoot = hostRoot }

// recentRootOr returns the host root when one was set, and workDir otherwise.
func recentRootOr(workDir string) string {
	if hostRecentRoot != "" {
		return hostRecentRoot
	}
	return workDir
}

// maxRecentFiles caps how many entries are kept per workspace.
const maxRecentFiles = 30

// recentFilesPath returns the on-disk path for workDir's recent-files list,
// keyed by a hash of its absolute path (mirrors server.socketDir /
// server.recoveryFilePath, which key per-workspace state the same way).
func recentFilesPath(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	h := sha256.Sum256([]byte(abs))
	dir := filepath.Join(home, ".indigo", "recent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%x.json", h[:8])), nil
}

// readRecentList reads the raw workspace-relative path list at p, most
// recently opened first. Returns nil if p doesn't exist or is malformed.
func readRecentList(p string) []string {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var rels []string
	if err := json.Unmarshal(data, &rels); err != nil {
		return nil
	}
	return rels
}

func writeRecentList(p string, rels []string) {
	data, err := json.Marshal(rels)
	if err != nil {
		return
	}
	os.WriteFile(p, data, 0600) //nolint:errcheck
}

// withRecentFilesLock runs fn while holding an exclusive advisory lock,
// serializing recordRecentFile's read-modify-write across concurrent indigo
// processes: each terminal window is a separate OS process (see CLAUDE.md's
// client/server architecture) sharing the same per-workspace recent-files
// file, so two windows opening files back-to-back without this lock could
// both read the same starting list and the second write silently clobbers
// the first's update — last-writer-wins data loss, not just a rare cosmetic
// glitch. The lock is taken on a sibling .lock file (never p itself) so
// holding it never blocks a plain read via readRecentList/loadRecentFiles.
// Best-effort: if the lock file can't be opened or locked, fn still runs
// unlocked rather than losing the update entirely — recording a recent file
// is not worth failing over.
func withRecentFilesLock(p string, fn func()) {
	f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fn()
		return
	}
	defer f.Close() //nolint:errcheck
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		fn()
		return
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck
	fn()
}

// dropString returns rels with any entry equal to s removed.
func dropString(rels []string, s string) []string {
	out := make([]string, 0, len(rels))
	for _, r := range rels {
		if r != s {
			out = append(out, r)
		}
	}
	return out
}

// recentRels returns workDir's recorded recent-files list, most-recently-opened
// first, exactly as it was written.
//
// No filtering happens here any more. Deciding whether an entry is still worth
// showing means a stat, the ignore set and a `git check-ignore` — all of which
// need the workspace, which the client may not be able to see. The list itself
// is not workspace state and stays here: it lives in the user's home directory
// and records what they have been editing, which is not something a container
// should own.
func recentRels(recentRoot string) []string {
	p, err := recentFilesPath(recentRoot)
	if err != nil {
		return nil
	}
	return readRecentList(p)
}

// recordRecentFile moves absPath to the front of the workspace's recent-files
// list, persisting the result. absPath outside workDir, or "" (untitled
// buffers), are ignored.
//
// workDir is the workspace as *this process* names it, for computing the
// relative path; recentRoot is where the workspace lives on the host, which is
// what the list is keyed by. Attached to a container the two differ, and the
// distinction is load-bearing: entries are stored relative so they mean the
// same thing from either side, while the key stays on the host so that two
// projects which both mount at /workspaces/api do not share one list.
func recordRecentFile(workDir, recentRoot, absPath string) {
	if absPath == "" {
		return
	}
	rel, err := filepath.Rel(workDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	// Not filtered here. Whether this path is ignored is a question about the
	// workspace, and answering it would mean a round trip on the path that runs
	// every time a buffer opens. The filter runs once, when the list is shown.
	p, err := recentFilesPath(recentRoot)
	if err != nil {
		return
	}
	withRecentFilesLock(p, func() {
		rels := append([]string{rel}, dropString(readRecentList(p), rel)...)
		if len(rels) > maxRecentFiles {
			rels = rels[:maxRecentFiles]
		}
		writeRecentList(p, rels)
	})
}
