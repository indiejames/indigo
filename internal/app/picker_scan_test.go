package app

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestOpenPickerDoesNotScanSynchronously is a regression test for a live
// bug report: opening the file picker (Ctrl+P) used to call collectFiles —
// a full recursive filepath.WalkDir over the whole workDir — directly
// inside Update, blocking the entire UI (including the keypress that would
// otherwise cancel or redirect it) until the walk finished. On a large or
// slow directory tree (the reported case: a user's home directory with no
// project-scoped root) this could hang the editor indefinitely with no
// feedback and no way to interrupt it.
//
// The scan must now happen in the tea.Cmd Update returns, not in Update
// itself: right after OpenPickerMsg, the picker must already be open but
// its global file list (picker.all) must still be empty/loading — proving
// Update returned without doing the walk — and only invoking the returned
// command (which is what Bubble Tea does in its own goroutine) actually
// performs it.
func TestOpenPickerDoesNotScanSynchronously(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := App{
		buffers: []client.Model{newReloadTestModel(1, filepath.Join(dir, "a.go"))},
		width:   80,
		height:  24,
		cfg:     &config.Config{},
		workDir: dir,
	}

	updated, cmd := a.Update(client.OpenPickerMsg{})
	a2 := updated.(App)

	if a2.picker == nil {
		t.Fatal("expected picker to be opened")
	}
	if !a2.picker.loadingAll {
		t.Error("loadingAll = false immediately after OpenPickerMsg, want true (scan deferred to the returned command)")
	}
	if len(a2.picker.all) != 0 {
		t.Errorf("picker.all = %v, want empty immediately after OpenPickerMsg (populated by the returned command instead)", a2.picker.all)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil scan command")
	}

	msg := cmd()
	pfm, ok := msg.(pickerFilesMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want pickerFilesMsg", msg)
	}
	found := false
	for _, f := range pfm.files {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("scanned files = %v, want to include a.go", pfm.files)
	}

	updated, _ = a2.Update(pfm)
	a3 := updated.(App)
	if a3.picker.loadingAll {
		t.Error("loadingAll still true after pickerFilesMsg applied")
	}
	if len(a3.picker.all) == 0 {
		t.Error("picker.all still empty after pickerFilesMsg applied")
	}
}

// TestNoBufferOpenCtrlPStartsScanWithoutBlocking is a regression test for
// the exact reported scenario: "indigo ." with no buffer open (e.g. an
// empty/all-ignored directory, or before the auto-open WindowSizeMsg
// handler has run) showed "No buffer open. Press ctrl+p to open a file.",
// and pressing ctrl+p appeared to do nothing. It didn't actually do
// nothing — collectFiles was running synchronously inside Update, so the
// picker never got a chance to render until the (potentially very slow)
// walk finished. ctrl+p must open the picker and return a scan command
// immediately, without walking the filesystem itself.
func TestNoBufferOpenCtrlPStartsScanWithoutBlocking(t *testing.T) {
	dir := t.TempDir()

	a := App{
		width:          80,
		height:         24,
		cfg:            &config.Config{},
		workDir:        dir,
		fileChangedIdx: -1,
	}

	updated, cmd := a.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	a2 := updated.(App)

	if a2.picker == nil {
		t.Fatal("expected ctrl+p to open the picker even with no buffers")
	}
	if !a2.picker.loadingAll {
		t.Error("loadingAll = false, want true (scan deferred to the returned command)")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil scan command")
	}
	if _, ok := cmd().(pickerFilesMsg); !ok {
		t.Errorf("cmd() did not produce a pickerFilesMsg")
	}
}

// TestPickerFilesMsgDiscardsStaleSeq mirrors this codebase's established
// staleness-guard pattern (grepResultsMsg, diagBrowserResultsMsg, ...): a
// scan whose picker has since been closed and reopened for a different
// directory must not clobber the newer picker's (still loading) state.
func TestPickerFilesMsgDiscardsStaleSeq(t *testing.T) {
	dir := t.TempDir()
	a := App{width: 80, height: 24, cfg: &config.Config{}, workDir: dir}

	a.picker = a.newDirectoryPicker()
	_ = a.startPickerFileScan(dir) // seq 1
	staleSeq := a.picker.seq

	// Reopen (e.g. the user closed and reopened the picker) — new seq.
	a.picker = a.newDirectoryPicker()
	_ = a.startPickerFileScan(dir) // seq 2

	updated, _ := a.Update(pickerFilesMsg{seq: staleSeq, files: []string{"stale.go"}})
	a2 := updated.(App)

	if !a2.picker.loadingAll {
		t.Error("loadingAll = false after a stale pickerFilesMsg was applied; it should have been discarded")
	}
	if len(a2.picker.all) != 0 {
		t.Errorf("picker.all = %v, want empty; a stale scan result must not be applied", a2.picker.all)
	}
}

// TestPickerFilesMsgRefreshesActiveSearch verifies that if the user starts
// typing a search query before the background scan finishes, the results
// are recomputed once the scan's files actually arrive, rather than
// staying stuck on whatever (empty) results existed while loading.
func TestPickerFilesMsgRefreshesActiveSearch(t *testing.T) {
	dir := t.TempDir()
	a := App{width: 80, height: 24, cfg: &config.Config{}, workDir: dir}

	a.picker = a.newDirectoryPicker()
	cmd := a.startPickerFileScan(dir)
	a.picker.setQuery("main") // user starts searching while still loading

	msg := cmd().(pickerFilesMsg)
	msg.files = []string{"cmd/indigo/main.go", "other.go"}

	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if len(a2.picker.filtered) != 1 || a2.picker.filtered[0] != "cmd/indigo/main.go" {
		t.Errorf("filtered = %v, want [cmd/indigo/main.go] (search refreshed once files arrived)", a2.picker.filtered)
	}
}

// TestPickerFileScanDoesNotRaceWithIgnoredDirsReload is a regression test
// (found in review) for a data race introduced by deferring collectFiles
// into a background command: collectFiles used to read the shared
// package-level ignoredDirs variable directly, but it now runs
// concurrently with the rest of Update on its own goroutine — including a
// config hot-reload's addIgnoredDirs, which reassigns ignoredDirs to a
// brand-new map. Run with -race; confirmed to flag a real race (both on
// the ignoredDirs variable itself and, more seriously, on the new map's
// internal state during construction) before startPickerFileScan started
// snapshotting ignoredDirs on the caller's goroutine and passing it into
// collectFiles as a parameter.
func TestPickerFileScanDoesNotRaceWithIgnoredDirsReload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := App{width: 80, height: 24, cfg: &config.Config{}, workDir: dir}
	a.picker = a.newDirectoryPicker()
	cmd := a.startPickerFileScan(dir)

	done := make(chan struct{})
	go func() {
		defer close(done)
		cmd() // runs collectFiles, same as Bubble Tea would on its own goroutine
	}()

	addIgnoredDirs([]string{"some-dir"}) // concurrent config-reload-style reassignment
	<-done
}
