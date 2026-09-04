package app

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestIgnoredDirsConcurrentAccess is a regression test for a
// race-detector-confirmed data race: ignoredDirs is reassigned by
// addIgnoredDirs on the main goroutine (config hot-reload, polled every 2s
// by watchConfig) while background goroutines read it — the file picker's
// scan and both workspace-grep backends. It bit the picker path first
// (fixed by snapshotting in startPickerFileScan), then both grep paths,
// which is why the bare variable is now unreachable outside
// ignoredDirsSnapshot/addIgnoredDirs.
//
// Run under -race; this fails loudly against any reader that goes back to
// touching the shared variable directly.
func TestIgnoredDirsConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	readers := []struct {
		name string
		fn   func()
	}{
		// Both workspace-grep backends' ignore-set reads.
		{"walkCandidateFiles", func() { walkCandidateFiles(dir) }},                      //nolint:errcheck
		{"searchWithRg", func() { searchWithRg(dir, "package", "", "", false, false) }}, //nolint:errcheck
		// The picker's own scan, and the recent-files filter.
		{"collectFiles", func() { collectFiles(dir, ignoredDirsSnapshot()) }},
		{"isInIgnoredDir", func() { isInIgnoredDir("pkg/a.go") }},
	}

	var wg sync.WaitGroup
	for _, r := range readers {
		wg.Add(2)
		go func(fn func()) {
			defer wg.Done()
			fn()
		}(r.fn)
		go func() {
			defer wg.Done()
			addIgnoredDirs([]string{"some-configured-dir"})
		}()
	}
	wg.Wait()
}
