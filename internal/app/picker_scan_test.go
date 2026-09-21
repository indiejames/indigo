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
// The scan must happen in the tea.Cmd Update returns, not in Update itself:
// right after OpenPickerMsg, the picker must already be open but its global
// file list (picker.all) must still be empty/loading, proving Update returned
// without doing the walk.
//
// The walk now runs on the server (see internal/workspacefs), which makes this
// guard stronger rather than weaker — the work Update must not do is a round
// trip as well as a walk. What this test no longer does is invoke the returned
// command: that needs a live server, and that half is covered by the RPC tests
// in internal/client. The deferral is what this test is named for and is what
// the reported bug was.
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
	if !a2.picker.loadingDir {
		t.Error("loadingDir = false immediately after OpenPickerMsg, want true (listing deferred too)")
	}
	if len(a2.picker.entries) != 0 {
		t.Errorf("picker.entries = %v, want empty immediately after OpenPickerMsg", a2.picker.entries)
	}
	if cmd == nil {
		t.Fatal("expected a non-nil scan command")
	}

	// Applying a result still clears the loading state.
	pfm := pickerFilesMsg{seq: a2.picker.seq, files: []string{"a.go"}}
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
// immediately, without touching the filesystem itself — which it now could not
// do anyway, since the workspace may be on the far side of a container.
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
	_ = a.startPickerFileScan(dir) // stamps the picker's seq
	a.picker.setQuery("main")      // user starts searching while still loading

	// The scan's result, as the server would have answered it. Built here
	// rather than by running the command, which now needs a live server.
	msg := pickerFilesMsg{seq: a.picker.seq, files: []string{"cmd/indigo/main.go", "other.go"}}

	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if len(a2.picker.filtered) != 1 || a2.picker.filtered[0] != "cmd/indigo/main.go" {
		t.Errorf("filtered = %v, want [cmd/indigo/main.go] (search refreshed once files arrived)", a2.picker.filtered)
	}
}

// The scan/ignore-set data race this file used to guard on the client moved
// with the code that had it: CollectFiles now runs on the server, so the
// concurrent pair is workspacefs.SetIgnoredDirs against workspacefs's own
// readers. TestIgnoredDirsConcurrentAccess in internal/workspacefs is that
// test, moved rather than dropped — the guard is still there, it is just no
// longer reachable from here, because nothing on this side walks the tree.
