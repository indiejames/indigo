package workspacefs

import (
	"os"
	"path/filepath"
)

// FilterExisting keeps the workspace-relative paths that are still worth
// offering: the file exists, it is not under an ignored directory, and git
// does not ignore it.
//
// This is the recent-files list's filter, and it runs here because every part
// of it needs the workspace — a stat, the ignore set, and a `git check-ignore`
// in the repository. The list itself is not workspace state and stays with the
// client: it lives in the user's home directory and describes what *they* have
// been editing, which is not something a container should own.
//
// git is consulted once for the whole batch rather than per path, which is why
// this takes a slice.
func FilterExisting(root string, rels []string) []string {
	candidates := make([]string, 0, len(rels))
	for _, rel := range rels {
		if IsInIgnoredDir(rel) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			continue
		}
		candidates = append(candidates, rel)
	}
	ignored := GitIgnoredSet(root, candidates)
	out := make([]string, 0, len(candidates))
	for _, rel := range candidates {
		if !ignored[rel] {
			out = append(out, rel)
		}
	}
	return out
}

// Stat reports whether path exists and whether it is a directory.
//
// A missing path is not an error: the caller that asks about a new file's
// parent directory treats "absent" as an offer to create it and anything else
// as a refusal, so the two must not arrive by the same channel.
func Stat(path string) (exists, isDir bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil
		}
		return false, false, err
	}
	return true, info.IsDir(), nil
}

// CreateDir creates dir and any missing parents.
func CreateDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
