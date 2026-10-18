package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

// TestEditsOnDiskDoNotLeakDirectoryWatches is a regression test. Editing a
// file nobody has open — a workspace edit or a move-to-file landing in it —
// called addPathWatch, and nothing ever removed it: each edit raised the
// directory's count for good and left a live watch on it. The watch could not
// do anything either, since handleExternalWrite ignores a path with no buffer.
func TestEditsOnDiskDoNotLeakDirectoryWatches(t *testing.T) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watcher.Close() }) //nolint:errcheck
	s := &editorService{
		buffers:    map[uint32]*bufferEntry{},
		dirWatches: map[string]int{},
		watcher:    watcher,
	}

	dir := t.TempDir()
	edited := filepath.Join(dir, "edited.go")
	if err := os.WriteFile(edited, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		items := []workspaceEditItem{{line: 0, col: 0, oldText: "hello", newText: "hello"}}
		if applied, _, err := s.applyWorkspaceEditsOnDisk(edited, 1, items); err != nil || applied != 1 {
			t.Fatalf("applyWorkspaceEditsOnDisk = %d, %v", applied, err)
		}
	}
	if err := s.appendTextToFile(1, filepath.Join(dir, "moved.go"), "func f() {}\n"); err != nil {
		t.Fatal(err)
	}

	if n := s.dirWatches[dir]; n != 0 {
		t.Errorf("dirWatches[%s] = %d after edits to files nobody has open, want 0", dir, n)
	}
	if s.dirWatched[dir] {
		t.Errorf("%s is still watched after edits to files nobody has open", dir)
	}
}
