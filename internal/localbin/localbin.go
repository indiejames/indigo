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
	// Normalize both so the dir == workspaceRoot boundary check below is a
	// reliable string comparison — filepath.Dir never returns a trailing
	// separator, so an uncleaned, trailing-separator workspaceRoot would
	// otherwise never match, and the walk would silently continue past the
	// intended workspace boundary into ancestor directories.
	startDir = filepath.Clean(startDir)
	workspaceRoot = filepath.Clean(workspaceRoot)
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

// PackageDir reports the directory whose node_modules supplied bin — the
// inverse of the path Resolve builds. Returns ("", false) for anything not
// shaped like "<dir>/node_modules/.bin/<cmd>", which is the right answer for a
// tool found on PATH.
//
// This exists so a caller can run that tool with the package as its working
// directory. Resolving the binary per-file but running it from the workspace
// root asserts two different things about which package a file belongs to, and
// tools notice: ESLint 9's flat config is discovered from the cwd upward, not
// from the linted file, so a package's own eslint.config.* would never be seen;
// and a typescript-eslint `parserOptions.project` given as a relative path is
// resolved against the cwd unless tsconfigRootDir says otherwise, so it would
// name the wrong tsconfig.
func PackageDir(bin string) (string, bool) {
	dotBin := filepath.Dir(bin)
	if filepath.Base(dotBin) != ".bin" {
		return "", false
	}
	nodeModules := filepath.Dir(dotBin)
	if filepath.Base(nodeModules) != "node_modules" {
		return "", false
	}
	return filepath.Dir(nodeModules), true
}
