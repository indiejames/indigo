// Package localbin resolves a locally-installed tool binary
// (node_modules/.bin/<cmd>) the way Node module resolution and tools like
// npm/prettier/eslint themselves do: by walking up from the file being
// processed, not just checking the workspace root once.
package localbin

import (
	"os"
	"path/filepath"
)

// Resolve walks up from startDir toward (and including) workspaceRoot,
// returning the first "<dir>/node_modules/.bin/<cmd>" that exists as a
// regular file, checking the directory closest to startDir first. This
// matters for a monorepo with non-hoisted per-package node_modules (a
// dedicated formatter/linter installed only in
// "services/foo/node_modules/.bin/", not at the workspace root) — a check
// against workspaceRoot alone would never find it even though it's
// genuinely available for files under that package.
//
// The walk stops once it reaches workspaceRoot (inclusive) or the
// filesystem root, whichever comes first; if startDir isn't under
// workspaceRoot at all, it stops at the filesystem root. Returns ("",
// false) if nothing is found.
func Resolve(startDir, workspaceRoot, cmd string) (string, bool) {
	dir := startDir
	for {
		candidate := filepath.Join(dir, "node_modules", ".bin", cmd)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
		if dir == workspaceRoot {
			return "", false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false // reached the filesystem root
		}
		dir = parent
	}
}
