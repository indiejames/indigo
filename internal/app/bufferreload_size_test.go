package app

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestBackgroundReloadedBufferIsSized is the regression for switching to a tab
// and seeing only "loading…" for a minute or more.
//
// A reload replaces the tab's Model with a freshly built one, which has never
// seen a WindowSizeMsg — and a Model with no width renders just "loading…". The
// handler sized it only when the reloaded tab was the active one, but the
// usual reload is of a background tab (a file changed on disk under a tab with
// no unsaved changes), so the tab stayed unsized until an unrelated resize.
func TestBackgroundReloadedBufferIsSized(t *testing.T) {
	a := App{
		buffers: []client.Model{
			client.New(&client.RPC{}, 1, "package a\n", 0, "/tmp/a.go", "/tmp", nil, false, 0),
			client.New(&client.RPC{}, 2, "package b\n", 0, "/tmp/b.go", "/tmp", nil, false, 0),
		},
		active: 0,
		cfg:    &config.Config{},
		width:  80,
		height: 24,
	}
	a.resizeAllBuffers()

	// Tab 1 — not the active one — is reloaded from disk.
	reloaded := client.New(&client.RPC{}, 2, "package b // changed on disk\n", 0, "/tmp/b.go", "/tmp", nil, false, 0)
	updated, _ := a.Update(bufferReloadedMsg{idx: 1, oldBufID: 2, model: reloaded})
	a = updated.(App)
	// Checked before the switch: ensureActiveSized would size it on arrival,
	// which would hide the reload handler's own omission.
	if !a.buffers[1].Sized() {
		t.Error("the reload handler left a background tab unsized")
	}

	// Then the user switches to it.
	updated, _ = a.Update(switchBufferMsg{idx: 1, line: -1, col: -1})
	a = updated.(App)

	if got := a.buffers[a.active].View().Content; strings.TrimSpace(got) == "loading…" {
		t.Fatal("switching to a tab reloaded in the background shows only \"loading…\"")
	}
	if got := a.buffers[a.active].View().Content; !strings.Contains(got, "changed on disk") {
		t.Errorf("reloaded tab does not show its new content:\n%s", got)
	}
}

// TestActiveBufferIsSizedWhateverPutItThere covers the backstop: a Model that
// reaches the active tab unsized, by any path, is sized on the next message
// rather than showing "loading…" until an unrelated resize.
func TestActiveBufferIsSizedWhateverPutItThere(t *testing.T) {
	a := App{
		buffers: []client.Model{client.New(&client.RPC{}, 1, "package a\n", 0, "/tmp/a.go", "/tmp", nil, false, 0)},
		cfg:     &config.Config{},
		width:   80,
		height:  24,
	}
	if a.buffers[0].Sized() {
		t.Fatal("test setup: a freshly built Model should start unsized")
	}
	updated, _ := a.Update(nil) // any message at all
	a = updated.(App)
	if !a.buffers[0].Sized() {
		t.Error("an unsized active buffer was not sized on the next message")
	}
}
