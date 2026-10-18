package agenttools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// substringPathTools take a "path" that is a filter matched as a substring,
// not a file to resolve, so a relative one is never ambiguous.
var substringPathTools = map[string]bool{"get_sync_events": true}

// checkRelativePath refuses a relative "path" in rawInput that could name more
// than one file: one in workDir, and one in another git worktree of the same
// repository that holds a file at that relative path too.
//
// One MCP process serves one workspace and resolves relative paths against it.
// A Claude Code agent working in a worktree (.claude/worktrees/...) shares its
// parent session's MCP process, so its relative path was quietly resolved into
// the main checkout: the edit landed there, and the agent, reading its own copy,
// saw no change and reported the tool as having lied. The tool cannot know
// which checkout the caller means, so it asks for an absolute path instead of
// guessing.
func checkRelativePath(workDir, name string, rawInput json.RawMessage) error {
	if substringPathTools[name] {
		return nil
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(rawInput, &in) != nil || in.Path == "" || filepath.IsAbs(in.Path) {
		return nil
	}
	var also []string
	for _, wt := range otherWorktrees(workDir) {
		if _, err := os.Stat(filepath.Join(wt, in.Path)); err == nil {
			also = append(also, wt)
		}
	}
	if len(also) == 0 {
		return nil
	}
	return fmt.Errorf("%q is ambiguous: this tool resolves relative paths against %s, but %s also exists in the git worktree(s) %s. "+
		"Pass an absolute path for the checkout you are working in",
		in.Path, workDir, in.Path, strings.Join(also, ", "))
}

// otherWorktrees returns the working trees of workDir's git repository other
// than workDir itself, read straight from the repository's metadata so a tool
// call costs no subprocess. A worktree whose directory no longer exists is
// left out, as is anything that cannot be read: this only ever adds a refusal,
// so failing to find a worktree just keeps the old behaviour.
func otherWorktrees(workDir string) []string {
	common := commonGitDir(workDir)
	if common == "" {
		return nil
	}
	self := canonical(workDir)
	var trees []string
	add := func(dir string) {
		if dir == "" || canonical(dir) == self {
			return
		}
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			trees = append(trees, dir)
		}
	}
	// The main working tree, unless the repository is bare.
	if filepath.Base(common) == ".git" {
		add(filepath.Dir(common))
	}
	entries, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(common, "worktrees", e.Name(), "gitdir"))
		if err != nil {
			continue
		}
		// gitdir holds the path of the worktree's .git file.
		add(filepath.Dir(strings.TrimSpace(string(b))))
	}
	return trees
}

// commonGitDir returns the repository directory shared by every worktree of
// the repository workDir is in, or "" when workDir is not in one.
func commonGitDir(workDir string) string {
	dotGit := filepath.Join(workDir, ".git")
	fi, err := os.Stat(dotGit)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return dotGit
	}
	// A linked worktree: .git is a file saying "gitdir: <repo>/.git/worktrees/<name>",
	// and that directory's commondir file points back at <repo>/.git.
	b, err := os.ReadFile(dotGit)
	if err != nil {
		return ""
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(workDir, gitDir)
	}
	c, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(c))
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common)
}

func canonical(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
