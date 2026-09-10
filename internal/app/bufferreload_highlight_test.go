package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/highlight"
)

// Distinct contents so a mix-up between the two buffers' highlight spans is
// visible in what gets rendered: the reloaded file leads with a comment line
// where the other buffer has a `package` keyword.
const (
	reloadHLActive  = "package main\n\nfunc alpha() {}\n"
	reloadHLOld     = "package main\n\nfunc beta() {}\n"
	reloadHLReload  = "// header comment\npackage main\n\nimport \"os\"\n\nfunc gamma(x int) string {\n\treturn \"z\"\n}\n"
	reloadHLBufW    = 80
	reloadHLBufH    = 24
	reloadHLTabRows = 1 // the tab bar takes one row once a second buffer exists
)

func newHighlightReloadModel(t *testing.T, bufID uint32, path, content string) client.Model {
	t.Helper()
	if highlight.New(path) == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}
	m := client.New(&client.RPC{}, bufID, content, 0, path, "/tmp", nil, false, 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: reloadHLBufW, Height: reloadHLBufH - reloadHLTabRows})
	return updated.(client.Model)
}

// drainToApp runs cmd and feeds every message it produces (unwrapping
// tea.BatchMsg, which is what Model.Init returns) back into a.Update, exactly
// as the Bubble Tea runtime would.
func drainToApp(t *testing.T, a *App, cmd tea.Cmd, depth int) {
	t.Helper()
	if cmd == nil || depth > 4 {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			drainToApp(t, a, sub, depth+1)
		}
		return
	}
	if msg == nil {
		return
	}
	updated, _ := a.Update(msg)
	*a = updated.(App)
}

// TestReloadHighlightDoesNotRepaintActiveBuffer is a regression test for a
// live bug: reloading a buffer after an external change (FileChangedMsg →
// doReloadBuffer) rebuilds its Model from scratch and calls Init() on it,
// whose reparseHighlight is the one highlight request that routinely
// originates from a buffer other than the active one. highlightMsg carried no
// bufID, so App's generic dispatch handed it to a.buffers[a.active] — and its
// per-Model seq counter matched (a fresh Model's first request is seq 1, as is
// an unedited active buffer's), so the active buffer accepted spans computed
// from a completely different file and repainted itself with them until its
// next reparse.
func TestReloadHighlightDoesNotRepaintActiveBuffer(t *testing.T) {
	a := App{
		buffers: []client.Model{
			newHighlightReloadModel(t, 1, "/tmp/a.go", reloadHLActive),
			newHighlightReloadModel(t, 2, "/tmp/b.go", reloadHLOld),
		},
		active: 0,
		cfg:    &config.Config{},
		width:  reloadHLBufW,
		height: reloadHLBufH,
	}
	drainToApp(t, &a, a.buffers[0].Init(), 0)
	drainToApp(t, &a, a.buffers[1].Init(), 0)
	activeBefore := a.buffers[0].View().Content

	// The background tab (idx 1) reloads: same path, fresh bufID, new content.
	reloaded := newHighlightReloadModel(t, 3, "/tmp/b.go", reloadHLReload)
	updated, cmd := a.Update(bufferReloadedMsg{idx: 1, oldBufID: 2, model: reloaded})
	a = updated.(App)
	drainToApp(t, &a, cmd, 0)

	if got := a.buffers[0].View().Content; got != activeBefore {
		t.Errorf("active buffer's rendering changed when an inactive buffer reloaded\nbefore:\n%s\nafter:\n%s",
			activeBefore, got)
	}
}

// TestReloadedBufferGetsItsOwnHighlight is the other half of the same bug: the
// reloaded buffer must actually receive the spans computed for it. Before the
// fix they went to the active buffer instead, leaving the reloaded one
// rendering unhighlighted text until the next edit forced a reparse.
func TestReloadedBufferGetsItsOwnHighlight(t *testing.T) {
	a := App{
		buffers: []client.Model{
			newHighlightReloadModel(t, 1, "/tmp/a.go", reloadHLActive),
			newHighlightReloadModel(t, 2, "/tmp/b.go", reloadHLOld),
		},
		active: 0,
		cfg:    &config.Config{},
		width:  reloadHLBufW,
		height: reloadHLBufH,
	}
	drainToApp(t, &a, a.buffers[0].Init(), 0)
	drainToApp(t, &a, a.buffers[1].Init(), 0)

	reloaded := newHighlightReloadModel(t, 3, "/tmp/b.go", reloadHLReload)
	updated, cmd := a.Update(bufferReloadedMsg{idx: 1, oldBufID: 2, model: reloaded})
	a = updated.(App)
	drainToApp(t, &a, cmd, 0)

	// A Model that has never been highlighted renders the same text with no
	// syntax colouring, so comparing against one is a theme-independent way to
	// assert that spans actually landed on the reloaded buffer.
	plain := newHighlightReloadModel(t, 3, "/tmp/b.go", reloadHLReload)
	if a.buffers[1].View().Content == plain.View().Content {
		t.Error("reloaded buffer rendered without syntax highlighting — its highlightMsg never reached it")
	}
}
