package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The report: during a rebase, git failed with "Unable to create
// .git/index.lock: File exists" while indigo-git was running. Its background
// re-diff ran porcelain `git diff HEAD`, which refreshes the index's stat cache
// and writes the index back under index.lock, with or without optional locks.

// TestGitCommandsTakeNoOptionalLocks: everything runGit runs carries both
// spellings of "no optional locks".
func TestGitCommandsTakeNoOptionalLocks(t *testing.T) {
	cmd := gitCommand("/repo", "ls-files", "--", "a.txt")
	if want := []string{"git", "--no-optional-locks", "ls-files", "--", "a.txt"}; !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %q, want %q", cmd.Args, want)
	}
	if !slices.Contains(cmd.Env, "GIT_OPTIONAL_LOCKS=0") {
		t.Error("GIT_OPTIONAL_LOCKS=0 is not in the command's environment")
	}
	if cmd.Dir != "/repo" {
		t.Errorf("dir = %q, want /repo", cmd.Dir)
	}
}

// TestTheBackgroundDiffNeverWritesTheIndex runs the re-diff against a
// repository whose files' stat information is stale, which is what makes
// `git diff HEAD` write a refreshed index, and checks the index is untouched.
func TestTheBackgroundDiffNeverWritesTheIndex(t *testing.T) {
	root := newRepo(t, "main", "a\n")
	for i := range 20 {
		writeFile(t, filepath.Join(root, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("%d\n", i))
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-q", "-m", "more")
	writeFile(t, filepath.Join(root, "a.txt"), "changed\n")

	// Same content, newer mtime: the index's cached stat no longer matches.
	later := time.Now().Add(time.Hour)
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if !e.IsDir() {
			if err := os.Chtimes(filepath.Join(root, e.Name()), later, later); err != nil {
				t.Fatal(err)
			}
		}
	}
	index := filepath.Join(root, ".git", "index")
	before, _ := os.ReadFile(index)
	beforeInfo, _ := os.Stat(index)

	for _, p := range []string{"f3.txt", "a.txt"} {
		out, err := runGit(root, diffHEADArgs(filepath.Join(root, p))...)
		if err != nil {
			t.Fatalf("diff of %s: %v", p, err)
		}
		lines := parseDiff(out).lines
		if p == "a.txt" && lines[1] != lineAdded {
			t.Errorf("a.txt's change is not shown: %v\n%s", lines, out)
		}
		if p == "f3.txt" && len(lines) != 0 {
			t.Errorf("unchanged f3.txt shows changes: %v\n%s", lines, out)
		}
	}

	after, _ := os.ReadFile(index)
	afterInfo, _ := os.Stat(index)
	if !bytes.Equal(before, after) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Error("the background diff rewrote .git/index, so it took index.lock")
	}
}

// The list is spelled out rather than read from busyMarkers, so dropping a
// marker from that fails here.
func TestARepositoryIsBusyWhileGitIsMidOperation(t *testing.T) {
	for _, marker := range []string{"index.lock", "rebase-merge", "rebase-apply", "MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "BISECT_LOG"} {
		dir := t.TempDir()
		if repoBusy(dir) {
			t.Fatal("an empty git directory reads as busy")
		}
		path := filepath.Join(dir, marker)
		var err error
		if marker == "rebase-merge" || marker == "rebase-apply" {
			err = os.Mkdir(path, 0o755)
		} else {
			err = os.WriteFile(path, nil, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !repoBusy(dir) {
			t.Errorf("not busy with %s present", marker)
		}
	}
}

// TestThePollWaitsForTheRepositoryToSettle: a burst of index writes, as a
// rebase makes, re-diffs once, after it ends, not once per write.
func TestThePollWaitsForTheRepositoryToSettle(t *testing.T) {
	var p repoPoll
	rediffs := 0
	tick := func(head, index string, busy bool) bool {
		branch, rediff := p.observe(head, index, busy)
		_ = branch
		if rediff {
			rediffs++
		}
		return rediff
	}
	tick("ref: main", "1", false) // first sight: not yet settled
	if !tick("ref: main", "1", false) {
		t.Fatal("a settled repository was not diffed")
	}
	rediffs = 0
	for i := 2; i < 12; i++ { // the index changes on every tick
		tick("ref: main", fmt.Sprint(i), false)
	}
	if rediffs != 0 {
		t.Errorf("re-diffed %d times while the index kept changing, want 0", rediffs)
	}
	if !tick("ref: main", "11", false) {
		t.Error("no re-diff once the index stopped changing")
	}
	if tick("ref: main", "11", false) {
		t.Error("re-diffed again with nothing changed")
	}
}

// TestThePollSkipsABusyRepositoryAndCatchesUp: nothing runs while a rebase is
// under way, the branch still follows HEAD, and the change made meanwhile is
// diffed once the rebase ends.
func TestThePollSkipsABusyRepositoryAndCatchesUp(t *testing.T) {
	var p repoPoll
	p.observe("ref: main", "1", false)
	p.observe("ref: main", "1", false)

	branch, _ := p.observe("abc1234", "2", true)
	if !branch {
		t.Error("the branch did not follow HEAD during a rebase")
	}
	for range 5 { // settled, but still busy
		if _, rediff := p.observe("abc1234", "2", true); rediff {
			t.Fatal("re-diffed while the repository was busy")
		}
	}
	if branch, rediff := p.observe("ref: main", "3", false); branch != true || rediff {
		t.Errorf("rebase just finished: branch=%v rediff=%v, want true, false (not settled)", branch, rediff)
	}
	if _, rediff := p.observe("ref: main", "3", false); !rediff {
		t.Error("the rebase's changes were never diffed")
	}
}
