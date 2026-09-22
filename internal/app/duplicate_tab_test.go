package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestRefPickerSameFileSwitchesInsteadOfOpeningNewTab is a regression test for
// a live bug: choosing a Find References (gr) result in the file already being
// edited opened a second tab onto the same server buffer, and that tab had no
// syntax highlighting. doOpenFileAtPos — shared by the reference picker, the
// symbol picker and OpenFileAtMsg — was the one open path without the
// already-open check its siblings make.
func TestRefPickerSameFileSwitchesInsteadOfOpeningNewTab(t *testing.T) {
	const path = "/tmp/refs.go"
	a := App{
		buffers: []client.Model{client.New(&client.RPC{}, 7, "package main\n\nfunc f() {}\n\nfunc g() { f() }\n", 0, path, "/tmp", nil, false, 0)},
		cfg:     &config.Config{},
		width:   80,
		height:  24,
		refPicker: newRefPicker("References", []client.ClientReference{
			{Path: path, Line: 4, Col: 11},
		}, 80, 24),
	}

	updated, cmd := a.handleRefPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	a = updated.(App)
	if cmd == nil {
		t.Fatal("enter on a reference produced no command")
	}
	msg := cmd()
	sw, ok := msg.(switchBufferMsg)
	if !ok {
		// A bufferOpenedMsg here means an OpenFile RPC was issued for a file
		// that is already open — the bug. (With a zero RPC it would panic or
		// error before getting that far, which is also a failure.)
		t.Fatalf("got %T, want switchBufferMsg", msg)
	}
	if sw.idx != 0 || sw.line != 4 || sw.col != 11 {
		t.Errorf("switch = {idx %d line %d col %d}, want {0 4 11}", sw.idx, sw.line, sw.col)
	}
	updated, _ = a.Update(sw)
	a = updated.(App)
	if len(a.buffers) != 1 {
		t.Fatalf("buffers = %d, want 1", len(a.buffers))
	}
}

// TestBufferOpenedForAlreadyOpenBufIDDoesNotAddTab covers the backstop: an
// open that raced another open of the same file arrives carrying a bufID some
// tab already holds. Two Models sharing a bufID break RoutableMsg dispatch
// (every bufID-tagged result goes to the first match), so the second must
// never be added.
func TestBufferOpenedForAlreadyOpenBufIDDoesNotAddTab(t *testing.T) {
	const path = "/tmp/dup.go"
	content := "package main\n\nfunc f() {}\n"
	a := App{
		buffers: []client.Model{client.New(&client.RPC{}, 7, content, 0, path, "/tmp", nil, false, 0)},
		cfg:     &config.Config{},
		width:   80,
		height:  24,
	}
	dup := client.New(&client.RPC{}, 7, content, 0, path, "/tmp", nil, false, 0)

	updated, cmd := a.Update(bufferOpenedMsg{model: dup, line: 2, col: 5})
	a = updated.(App)
	if len(a.buffers) != 1 {
		t.Fatalf("buffers = %d, want 1 (duplicate tab added for bufID 7)", len(a.buffers))
	}
	if cmd == nil {
		t.Fatal("no switch command for the already-open buffer")
	}
	sw, ok := cmd().(switchBufferMsg)
	if !ok || sw.idx != 0 || sw.line != 2 || sw.col != 5 {
		t.Errorf("got %+v, want switchBufferMsg{idx 0 line 2 col 5}", sw)
	}
}
