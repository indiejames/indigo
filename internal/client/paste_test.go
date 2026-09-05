package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestPasteMsgIsHandled is a regression test for a Bubble Tea v1 → v2 gap.
//
// v1 delivered bracketed paste as an ordinary KeyMsg (Paste set, text in
// Runes), so it fell through the same "append the typed text" branch as
// normal typing. v2 emits a separate tea.PasteMsg that matches no KeyMsg
// case, so after the v2 migration paste silently did nothing anywhere —
// including into the buffer itself. Bracketed paste is on by default in v2,
// so this is the path a real terminal paste actually takes.
func TestPasteMsgIsHandled(t *testing.T) {
	t.Run("insert mode inserts the text", func(t *testing.T) {
		m := newTestModel("")
		m.rpc = &RPC{} // zero-value RPC: edits need one; the capnp call just fails
		m.mode = ModeInsert
		got, _ := m.Update(tea.PasteMsg{Content: "pasted"})
		if content := got.(Model).buf.Content(); !strings.Contains(content, "pasted") {
			t.Errorf("buffer = %q, want it to contain the pasted text", content)
		}
	})

	t.Run("multi-line paste keeps its newlines", func(t *testing.T) {
		m := newTestModel("")
		m.rpc = &RPC{} // zero-value RPC: edits need one; the capnp call just fails
		m.mode = ModeInsert
		got, _ := m.Update(tea.PasteMsg{Content: "one\ntwo\nthree"})
		mm := got.(Model)
		if mm.buf.LineCount() < 3 {
			t.Errorf("line count = %d, want at least 3: a multi-line paste should create lines, "+
				"not be flattened", mm.buf.LineCount())
		}
	})

	t.Run("command mode appends to the prompt", func(t *testing.T) {
		m := newTestModel("")
		m.mode = ModeCommand
		m.cmdBuf = "w"
		got, _ := m.Update(tea.PasteMsg{Content: "q"})
		if b := got.(Model).cmdBuf; b != "wq" {
			t.Errorf("cmdBuf = %q, want %q", b, "wq")
		}
	})

	t.Run("search mode appends to the query", func(t *testing.T) {
		m := newTestModel("hello\n")
		m.mode = ModeSearch
		m.searchQuery = "he"
		got, _ := m.Update(tea.PasteMsg{Content: "llo"})
		if q := got.(Model).searchQuery; q != "hello" {
			t.Errorf("searchQuery = %q, want %q", q, "hello")
		}
	})

	t.Run("normal mode ignores it", func(t *testing.T) {
		// Matches v1, where the whole pasted string arrived as one key event,
		// matched no binding, and was dropped. Inserting it instead would let
		// a stray paste silently modify the buffer outside insert mode.
		m := newTestModel("abc\n")
		got, _ := m.Update(tea.PasteMsg{Content: "xyz"})
		if content := got.(Model).buf.Content(); strings.Contains(content, "xyz") {
			t.Errorf("normal-mode paste modified the buffer: %q", content)
		}
	})

	t.Run("empty paste is a no-op", func(t *testing.T) {
		m := newTestModel("abc\n")
		m.rpc = &RPC{}
		m.mode = ModeInsert
		got, _ := m.Update(tea.PasteMsg{Content: ""})
		if content := got.(Model).buf.Content(); content != "abc\n" {
			t.Errorf("buffer = %q, want it unchanged", content)
		}
	})
}
