package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
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

// loadRecentFiles returns workDir's recent-files list, most-recently-opened
// first, skipping any entry whose file no longer exists on disk.
func loadRecentFiles(workDir string) []string {
	p, err := recentFilesPath(workDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, rel := range readRecentList(p) {
		if _, err := os.Stat(filepath.Join(workDir, rel)); err == nil {
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
