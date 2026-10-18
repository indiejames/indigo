package server

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/plugin"
)

// TestPluginEditIsRebasedPastConcurrentTyping: a plugin reads the buffer and
// edits it in two calls, and text typed in between used to shift where its
// edit landed — here, a replace of "world" would have hit "o wor". Given the
// version it read, the edit lands on the text it was computed against.
func TestPluginEditIsRebasedPastConcurrentTyping(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello world\n", 1)
	content, version, generation, err := s.PluginReadBuffer(1)
	if err != nil || content != "hello world\n" {
		t.Fatalf("PluginReadBuffer = %q, %v", content, err)
	}
	// Typed in a window while the plugin works.
	if err := sendOp(t, cl, 1, entry.buf.Version(), document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "say ",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PluginApplyEdit(1, version, generation, true, []plugin.TextEdit{{
		FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 11, NewText: "there",
	}}); err != nil {
		t.Fatalf("PluginApplyEdit: %v", err)
	}
	if got := entry.buf.Content(); got != "say hello there\n" {
		t.Fatalf("buffer = %q, want the plugin's replace to land on \"world\"", got)
	}
}

// TestPluginEditOutsideTheBufferIsRefused: Apply clamps a position that does
// not exist, so a plugin edit computed against other text went somewhere it
// never named. It is now an error, and the buffer is left alone.
func TestPluginEditOutsideTheBufferIsRefused(t *testing.T) {
	s, entry, _ := newOTTestService(t, "hi\n", 1)
	if err := s.PluginApplyEdit(1, 0, 0, false, []plugin.TextEdit{{
		FromLine: 5, FromCol: 0, ToLine: 5, ToCol: 0, NewText: "X",
	}}); err == nil {
		t.Fatalf("an edit at line 5 of a 2-line buffer was accepted: %q", entry.buf.Content())
	}
	if got := entry.buf.Content(); got != "hi\n" {
		t.Fatalf("buffer = %q, want it unchanged", got)
	}
	if q := queueFor(s, entry, 1); len(q) != 0 {
		t.Errorf("a refused edit was queued for delivery: %+v", q)
	}
}

// TestPluginEditFromAVersionNoLongerHeldIsRefused: rebasing needs the ops since
// baseVersion. A version the buffer has never reached (it was replaced since,
// and restarted its numbering) or whose ops were trimmed cannot be rebased
// from, and applying as given would be the old bug again.
func TestPluginEditFromAVersionNoLongerHeldIsRefused(t *testing.T) {
	s, entry, _ := newOTTestService(t, "hi\n", 1)
	if err := s.PluginApplyEdit(1, 40, 0, true, []plugin.TextEdit{{NewText: "X"}}); err == nil {
		t.Fatalf("an edit from version 40 of a version-0 buffer was accepted: %q", entry.buf.Content())
	}
}

// TestPluginEditFromBeforeASwapIsRefused: a wholesale swap (a reload, a SaveAs)
// restarts the buffer's version count. Once the new buffer has been edited
// past the version a plugin read, that version names a real point in the new
// history, and the edit used to be rebased past ops that have nothing to do
// with the text it was computed against, then applied. The generation read
// with the version tells the two buffers apart.
func TestPluginEditFromBeforeASwapIsRefused(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello world\n", 1)
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "> ",
	}); err != nil {
		t.Fatal(err)
	}
	_, version, generation, err := s.PluginReadBuffer(1)
	if err != nil || version == 0 {
		t.Fatalf("PluginReadBuffer = version %d, %v; the test needs a version past 0", version, err)
	}

	// The buffer is replaced, the way ReloadBuffer does it, and then edited
	// past the version the plugin read.
	s.mu.Lock()
	entry.buf = document.New(entry.buf.Path(), "something else entirely\n")
	entry.generation++
	resetOutgoing(entry)
	s.mu.Unlock()
	for range version {
		if err := sendOpGen(t, cl, 1, generation+1, entry.buf.Version(), document.Op{
			Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "#",
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := entry.buf.Content()

	err = s.PluginApplyEdit(1, version, generation, true, []plugin.TextEdit{{
		FromLine: 0, FromCol: 8, ToLine: 0, ToCol: 13, NewText: "there",
	}})
	if err == nil {
		t.Fatalf("an edit computed before the swap was applied to the new buffer: %q", entry.buf.Content())
	}
	if got := entry.buf.Content(); got != before {
		t.Errorf("buffer = %q after a refused edit, want %q", got, before)
	}
}
