package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// withTempHome points os.UserHomeDir at a fresh temp dir for the duration of
// the test, so recentFilesPath doesn't touch the real ~/.indigo.
func withTempHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	old := os.Getenv("HOME")
	os.Setenv("HOME", home)                      //nolint:errcheck
	t.Cleanup(func() { os.Setenv("HOME", old) }) //nolint:errcheck
}

func TestDropString(t *testing.T) {
	got := dropString([]string{"a", "b", "a", "c"}, "a")
	want := []string{"b", "c"}
	if len(got) != len(want) {
		t.Fatalf("dropString = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dropString = %v, want %v", got, want)
		}
	}
}

func TestRecordRecentFileMostRecentFirstAndDeduped(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(workDir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	recordRecentFile(workDir, filepath.Join(workDir, "a.go"))
	recordRecentFile(workDir, filepath.Join(workDir, "b.go"))
	recordRecentFile(workDir, filepath.Join(workDir, "c.go"))
	// Re-opening a.go should move it back to the front, not duplicate it.
	recordRecentFile(workDir, filepath.Join(workDir, "a.go"))

	got := loadRecentFiles(workDir)
	want := []string{"a.go", "c.go", "b.go"}
	if len(got) != len(want) {
		t.Fatalf("loadRecentFiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("loadRecentFiles = %v, want %v", got, want)
		}
	}
}

func TestRecordRecentFileCapsLength(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()
	for i := 0; i < maxRecentFiles+5; i++ {
		name := filepath.Join(workDir, "f"+string(rune('a'+i%26))+".go")
		os.WriteFile(name, nil, 0644) //nolint:errcheck
		recordRecentFile(workDir, name)
	}
	got := loadRecentFiles(workDir)
	if len(got) > maxRecentFiles {
		t.Fatalf("loadRecentFiles returned %d entries, want <= %d", len(got), maxRecentFiles)
	}
}

func TestLoadRecentFilesSkipsDeleted(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()
	keepPath := filepath.Join(workDir, "keep.go")
	goneePath := filepath.Join(workDir, "gone.go")
	os.WriteFile(keepPath, nil, 0644)  //nolint:errcheck
	os.WriteFile(goneePath, nil, 0644) //nolint:errcheck

	recordRecentFile(workDir, goneePath)
	recordRecentFile(workDir, keepPath)

	if err := os.Remove(goneePath); err != nil {
		t.Fatal(err)
	}

	got := loadRecentFiles(workDir)
	if len(got) != 1 || got[0] != "keep.go" {
		t.Fatalf("loadRecentFiles = %v, want [keep.go]", got)
	}
}

func TestRecordRecentFileIgnoresOutsideWorkDirAndUntitled(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()

	recordRecentFile(workDir, "")
	recordRecentFile(workDir, "/etc/hosts")

	if got := loadRecentFiles(workDir); len(got) != 0 {
		t.Fatalf("loadRecentFiles = %v, want empty", got)
	}
}

// TestRecordRecentFileExcludesGitInternalPath is a regression test: opening
// a file like .git/COMMIT_EDITMSG (e.g. via the indigo-git plugin) must not
// pollute the recent-files list, since it's not a project file.
func TestRecordRecentFileExcludesGitInternalPath(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	commitMsgPath := filepath.Join(workDir, ".git", "COMMIT_EDITMSG")
	os.WriteFile(commitMsgPath, []byte("msg"), 0644) //nolint:errcheck

	recordRecentFile(workDir, commitMsgPath)

	if got := loadRecentFiles(workDir); len(got) != 0 {
		t.Fatalf("loadRecentFiles = %v, want empty (.git paths must be excluded)", got)
	}
}

// runGit runs a git command in dir, failing the test on error.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRecordRecentFileExcludesGitignoredPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	withTempHome(t)
	workDir := t.TempDir()
	runGit(t, workDir, "init", "-q")
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("build/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, "build"), 0755); err != nil {
		t.Fatal(err)
	}
	ignoredPath := filepath.Join(workDir, "build", "output.go")
	os.WriteFile(ignoredPath, nil, 0644) //nolint:errcheck
	keptPath := filepath.Join(workDir, "main.go")
	os.WriteFile(keptPath, nil, 0644) //nolint:errcheck

	recordRecentFile(workDir, ignoredPath)
	recordRecentFile(workDir, keptPath)

	got := loadRecentFiles(workDir)
	want := []string{"main.go"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("loadRecentFiles = %v, want %v (build/output.go must be gitignored out)", got, want)
	}
}

// TestRecordRecentFileConcurrentWritesDontLoseUpdates is a regression test
// for a cross-process data race: recordRecentFile used to do an unlocked
// read-modify-write of the recent-files JSON file. Each terminal window is a
// separate OS process sharing the same per-workspace file (see CLAUDE.md's
// client/server architecture), so two windows opening files back-to-back
// could both read the same starting list and the second write would
// silently clobber the first's update — last-writer-wins data loss. Many
// goroutines here simulate that (goroutines instead of real processes, since
// flock is scoped to the open file description, not the process, so it
// serializes concurrent goroutines the same way it would concurrent
// processes). Every recorded file must survive.
func TestRecordRecentFileConcurrentWritesDontLoseUpdates(t *testing.T) {
	withTempHome(t)
	workDir := t.TempDir()

	const n = 20 // must stay <= maxRecentFiles or the cap interferes with the "nothing lost" assertion
	paths := make([]string, n)
	for i := 0; i < n; i++ {
		paths[i] = filepath.Join(workDir, fmt.Sprintf("f%d.go", i))
		if err := os.WriteFile(paths[i], nil, 0644); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	for _, p := range paths {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			recordRecentFile(workDir, p)
		}(p)
	}
	wg.Wait()

	got := loadRecentFiles(workDir)
	if len(got) != n {
		t.Fatalf("loadRecentFiles returned %d entries, want all %d (a concurrent read-modify-write must not lose updates)", len(got), n)
	}
	seen := make(map[string]bool, n)
	for _, rel := range got {
		seen[rel] = true
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("f%d.go", i)
		if !seen[name] {
			t.Errorf("recorded file %q missing from final list %v", name, got)
		}
	}
}

// TestLoadRecentFilesSelfHealsNewlyGitignoredEntry verifies that an entry
// which was fine when recorded, but is later added to .gitignore, drops out
// on the next load without needing any migration of the persisted list.
func TestLoadRecentFilesSelfHealsNewlyGitignoredEntry(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	withTempHome(t)
	workDir := t.TempDir()
	runGit(t, workDir, "init", "-q")
	genPath := filepath.Join(workDir, "generated.go")
	os.WriteFile(genPath, nil, 0644) //nolint:errcheck

	recordRecentFile(workDir, genPath)
	if got := loadRecentFiles(workDir); len(got) != 1 {
		t.Fatalf("loadRecentFiles before gitignore = %v, want [generated.go]", got)
	}

	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("generated.go\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := loadRecentFiles(workDir); len(got) != 0 {
		t.Fatalf("loadRecentFiles after gitignore = %v, want empty", got)
	}
}
