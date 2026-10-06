package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Saving is now where a new file comes into existence, since opening a
// non-existent path no longer creates one. That includes creating the
// directories on the way to it, which startup used to do.
func TestAtomicWriteFileCreatesMissingDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "new.txt")

	if err := atomicWriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("content = %q, want %q", got, "hello\n")
	}
}

// An existing file keeps its permissions, and the added MkdirAll must not
// disturb the directory it is already in.
func TestAtomicWriteFilePreservesModeAndDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "there.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := atomicWriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600 preserved", fi.Mode().Perm())
	}
	after, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Errorf("directory mode changed from %o to %o", before.Mode().Perm(), after.Mode().Perm())
	}
}

// Opening a path that does not exist gives an empty buffer and creates
// nothing, so the whole chain — startup, open, quit — leaves no file behind.
// Only Save brings it into existence.
func TestLoadContentDoesNotCreateAMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never-written.txt")
	s := &editorService{}

	content, fromRecovery, crlf, err := s.loadContent(path)
	if err != nil {
		t.Fatalf("loadContent: %v", err)
	}
	if content != "" || fromRecovery || crlf {
		t.Errorf("got content=%q fromRecovery=%v crlf=%v, want an empty buffer", content, fromRecovery, crlf)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("%s exists after loadContent; opening must not create it", path)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) != 0 {
		t.Errorf("directory is not empty after opening a missing file: %v", entries)
	}
}

// A new file in a directory that does not exist yet cannot have its directory
// watched at open time. That failed watch used to be counted as registered,
// so it was never retried and external changes to the file, once saved, went
// unnoticed for the life of the buffer. It must stay unregistered until the
// save creates the directory, and be live afterwards.
func TestPathWatchForMissingDirectoryIsRetriedAfterSave(t *testing.T) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watcher.Close() }) //nolint:errcheck
	s := &editorService{watcher: watcher, dirWatches: make(map[string]int)}

	dir := filepath.Join(t.TempDir(), "a", "b")
	path := filepath.Join(dir, "new.txt")

	s.addPathWatch(path)
	if s.dirWatched[dir] {
		t.Fatal("watch on a directory that does not exist was recorded as registered")
	}

	if err := atomicWriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("atomicWriteFile: %v", err)
	}
	s.retryPathWatch(path)
	if !s.dirWatched[dir] {
		t.Fatal("watch was not registered after the save created the directory")
	}

	// And it is a real watch: an external write to the file is seen.
	drain(watcher)
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
wait:
	for {
		select {
		case ev := <-watcher.Events:
			if filepath.Clean(ev.Name) == path {
				break wait
			}
		case err := <-watcher.Errors:
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatal("no fsnotify event for an external write after the retried watch")
		}
	}

	// Dropping the last path removes the watch and its bookkeeping.
	s.removePathWatch(path)
	if s.dirWatched[dir] || s.dirWatches[dir] != 0 {
		t.Errorf("after removePathWatch: dirWatched=%v dirWatches=%d, want both cleared", s.dirWatched[dir], s.dirWatches[dir])
	}
}

func drain(w *fsnotify.Watcher) {
	for {
		select {
		case <-w.Events:
		default:
			return
		}
	}
}
