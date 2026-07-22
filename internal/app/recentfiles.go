package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

// isInIgnoredDir reports whether rel has a path component the file picker
// already treats as non-project noise (.git, vendor, node_modules, ...) —
// same ignoredDirs set filepicker.go uses to hide them from Browse/search.
func isInIgnoredDir(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if ignoredDirs[part] {
			return true
		}
	}
	return false
}

// gitIgnoredSet returns the subset of rels (workspace-relative paths) that
// git considers ignored in workDir, via a single `git check-ignore --stdin`
// call — mirroring how workspace search already defers to git/ripgrep
// rather than reimplementing gitignore parsing. Returns nil (nothing
// filtered) if git isn't on PATH, workDir isn't a repo, or any other fatal
// error occurs; a plain "no matches" is a normal, empty result.
func gitIgnoredSet(workDir string, rels []string) map[string]bool {
	if len(rels) == 0 {
		return nil
	}
	cmd := exec.Command("git", "-C", workDir, "check-ignore", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return nil // git missing, not a repo, or another fatal error
		}
	}
	ignored := make(map[string]bool, len(rels))
	for _, line := range strings.Split(string(out), "\n") {
		if line != "" {
			ignored[line] = true
		}
	}
	return ignored
}

// loadRecentFiles returns workDir's recent-files list, most-recently-opened
// first, skipping any entry whose file no longer exists on disk, lives
// under an ignored directory (.git, vendor, ...), or is gitignored.
func loadRecentFiles(workDir string) []string {
	p, err := recentFilesPath(workDir)
	if err != nil {
		return nil
	}
	var candidates []string
	for _, rel := range readRecentList(p) {
		if isInIgnoredDir(rel) {
			continue
		}
		if _, err := os.Stat(filepath.Join(workDir, rel)); err != nil {
			continue
		}
		candidates = append(candidates, rel)
	}
	ignored := gitIgnoredSet(workDir, candidates)
	var out []string
	for _, rel := range candidates {
		if !ignored[rel] {
			out = append(out, rel)
		}
	}
	return out
}

// recordRecentFile moves absPath to the front of workDir's recent-files
// list, persisting the result. absPath outside workDir, or "" (untitled
// buffers), are ignored.
func recordRecentFile(workDir, absPath string) {
	if absPath == "" {
		return
	}
	rel, err := filepath.Rel(workDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return
	}
	if isInIgnoredDir(rel) {
		return
	}
	if ignored := gitIgnoredSet(workDir, []string{rel}); ignored[rel] {
		return
	}
	p, err := recentFilesPath(workDir)
	if err != nil {
		return
	}
	rels := append([]string{rel}, dropString(readRecentList(p), rel)...)
	if len(rels) > maxRecentFiles {
		rels = rels[:maxRecentFiles]
	}
	writeRecentList(p, rels)
}
