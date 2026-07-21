package server

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TestAddPathWatchSurvivesRepeatedExternalReplace is a regression test for a
// bug where external tools that replace a watched file via unlink+create
// (as `git checkout` does, rather than an in-place write) could permanently
// and silently kill the fsnotify watch after just one or two replacements —
// so a buffer's open file stopped picking up further external changes (e.g.
// switching git branches back and forth) with no way to recover short of
// closing and reopening the file. Watching the containing directory
// (addPathWatch/removePathWatch) rather than the file itself avoids this,
// since the directory's inode is never replaced.
func TestAddPathWatchSurvivesRepeatedExternalReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("v0"), 0644); err != nil {
		t.Fatal(err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close() //nolint:errcheck

	s := &editorService{watcher: watcher, dirWatches: make(map[string]int)}
	s.addPathWatch(path)
	defer s.removePathWatch(path)

	const n = 5
	seen := make(chan struct{}, n)
	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 0
		for count < n {
			select {
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Clean(ev.Name) == path && ev.Has(fsnotify.Create) {
					count++
					seen <- struct{}{}
				}
			case <-time.After(3 * time.Second):
				return
			}
		}
	}()

	// Replace the file the way `git checkout` does: unlink, then create a
	// fresh inode at the same path — not an in-place write or rename.
	for i := 1; i <= n; i++ {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("v%d", i)), 0644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	<-done
	close(seen)
	got := len(seen)
	if got != n {
		t.Errorf("observed %d/%d external file replacements; directory watch may have been stranded", got, n)
	}
}
