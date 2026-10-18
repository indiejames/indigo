package agenttools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepoWithWorktree makes a git repository holding a.go, with a linked
// worktree nested inside it the way Claude Code places one
// (.claude/worktrees/agent), and returns both directories.
func newRepoWithWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	main, _ = filepath.EvalSymlinks(t.TempDir())
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(main, "init", "-q")
	if err := os.WriteFile(filepath.Join(main, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", "a.go")
	git(main, "commit", "-q", "-m", "init")
	wt = filepath.Join(main, ".claude", "worktrees", "agent")
	git(main, "worktree", "add", "-q", wt)
	return main, wt
}

func pathInput(p string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"path": p, "reason": "r", "old_text": "a", "new_text": "b"})
	return b
}

// TestRelativePathIsRefusedWhenAWorktreeAlsoHasTheFile is the regression test
// for an agent in a worktree editing the main checkout: the MCP process
// resolved its relative path against the workspace it was started in, the edit
// landed there, and the agent's own copy never changed.
func TestRelativePathIsRefusedWhenAWorktreeAlsoHasTheFile(t *testing.T) {
	main, wt := newRepoWithWorktree(t)

	// Through ExecTool, so the guard is proven wired in and not just written.
	// It refuses before any RPC, so no server is needed.
	out, isErr := ExecTool(t.Context(), nil, AlwaysApprove{}, main, "apply_edits", pathInput("a.go"))
	if !isErr || !strings.Contains(out, "ambiguous") || !strings.Contains(out, wt) {
		t.Fatalf("relative path with a worktree holding the same file: got %q (error %v), want a refusal naming %s", out, isErr, wt)
	}

	// The other direction: an MCP process started in the worktree, and a
	// relative path the main checkout also has.
	if err := checkRelativePath(wt, "read_file", pathInput("a.go")); err == nil || !strings.Contains(err.Error(), main) {
		t.Errorf("from the worktree: got %v, want a refusal naming %s", err, main)
	}
}

func TestRelativePathIsAcceptedWhenItIsNotAmbiguous(t *testing.T) {
	main, wt := newRepoWithWorktree(t)
	if err := os.WriteFile(filepath.Join(main, "only_here.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, tool, path string
	}{
		{"absolute path", "apply_edits", filepath.Join(wt, "a.go")},
		{"file only in this checkout", "apply_edits", "only_here.go"},
		{"no path", "get_diagnostics", ""},
		{"a substring filter, not a file", "get_sync_events", "a.go"},
	} {
		if err := checkRelativePath(main, tc.tool, pathInput(tc.path)); err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
	}

	// A repository with no other worktree is never ambiguous.
	if err := checkRelativePath(t.TempDir(), "apply_edits", pathInput("a.go")); err != nil {
		t.Errorf("no repository: refused: %v", err)
	}
}

// TestOtherWorktreesSkipsARemovedWorktree covers a worktree deleted without
// `git worktree remove`, whose metadata outlives its directory.
func TestOtherWorktreesSkipsARemovedWorktree(t *testing.T) {
	main, wt := newRepoWithWorktree(t)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if got := otherWorktrees(main); len(got) != 0 {
		t.Errorf("otherWorktrees = %v, want none once the worktree's directory is gone", got)
	}
}
