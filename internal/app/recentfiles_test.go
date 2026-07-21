package app

import (
	"os"
	"path/filepath"
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
