package workspacefs

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitIgnoredSet returns the subset of rels (workspace-relative paths) that
// git considers ignored in workDir, via a single `git check-ignore --stdin`
// call — mirroring how workspace search already defers to git/ripgrep
// rather than reimplementing gitignore parsing. Returns nil (nothing
// filtered) if git isn't on PATH, workDir isn't a repo, or any other fatal
// error occurs; a plain "no matches" is a normal, empty result.
func GitIgnoredSet(workDir string, rels []string) map[string]bool {
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

// IsInIgnoredDir reports whether rel has a path component the file picker
// already treats as non-project noise (.git, vendor, node_modules, ...) —
// same ignoredDirs set the picker uses to hide them from Browse/search.
func IsInIgnoredDir(rel string) bool {
	ignored := IgnoredDirs()
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if ignored[part] {
			return true
		}
	}
	return false
}
