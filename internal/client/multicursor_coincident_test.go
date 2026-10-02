package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// collapseTwoCursors rebases two cursors past a remote delete that spans both,
// which is how they come to share a position in practice.
func collapseTwoCursors(t *testing.T) Model {
	t.Helper()
	m := newTestModel("abcdef\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 2}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 0, Col: 4}, goalCol: -1}}

	op := document.Op{Type: document.OpDelete, FromLine: 0, FromCol: 1, ToLine: 0, ToCol: 5}
	m.cursor = document.ShiftPos(m.cursor, op)
	for i := range m.extraCursors {
		m.extraCursors[i].pos = document.ShiftPos(m.extraCursors[i].pos, op)
	}
	m.buf.Apply(op)

	if m.cursor != m.extraCursors[0].pos {
		t.Fatalf("fixture did not collapse the cursors: %v vs %v", m.cursor, m.extraCursors[0].pos)
	}
	return m
}

// Two cursors at one position insert once. They are a single caret on screen,
// so inserting per cursor doubles what the user typed.
func TestInsertAtCoincidentCursorsInsertsOnce(t *testing.T) {
	m := collapseTwoCursors(t)

	got, _ := applyInsertToAllCursors(m, "X")

	if got.buf.Line(0) != "aXf" {
		t.Errorf("line = %q, want %q (inserted once, not once per coincident cursor)", got.buf.Line(0), "aXf")
	}
	if len(got.extraCursors) != 0 {
		t.Errorf("got %d extra cursors, want 0 — the duplicate should be gone", len(got.extraCursors))
	}
}

// Paste must dedupe before deciding whether to distribute, or the piece count
// agrees with a cursor count that no longer exists and the tail is dropped.
func TestPasteDoesNotDistributeOntoCollapsedCursors(t *testing.T) {
	withFakeClipboard(t, "P\nQ")
	m := collapseTwoCursors(t)
	m.multiYank = []string{"P", "Q"} // two pieces, but the cursors are now one

	got, _ := pasteAtAllCursors(m, "P\nQ")

	// One cursor remains, so the whole clipboard goes in at it.
	want := []string{"aP", "Qf", ""}
	if n := got.buf.LineCount(); n != len(want) {
		t.Fatalf("LineCount = %d, want %d; buffer: %q", n, len(want), got.buf.Content())
	}
	for i, w := range want {
		if got.buf.Line(i) != w {
			t.Errorf("line %d = %q, want %q", i, got.buf.Line(i), w)
		}
	}
}

// Position mode must ignore selections: two cursors at one position with
// different selections still insert once. Range mode would keep both.
func TestDedupeByPositionIgnoresSelections(t *testing.T) {
	m := newTestModel("abcdef\n")
	m.cursor = document.Pos{Line: 0, Col: 3}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 3}}
	m.extraCursors = []ExtraCursor{{
		pos: document.Pos{Line: 0, Col: 3},
		sel: &Selection{Anchor: document.Pos{Line: 0, Col: 2}, Head: document.Pos{Line: 0, Col: 3}},
	}}

	byRange := m
	byRange.dedupeCursors(dedupeBySelection)
	if len(byRange.extraCursors) != 1 {
		t.Errorf("dedupeBySelection dropped a cursor whose selection differs; got %d extras, want 1",
			len(byRange.extraCursors))
	}

	byPos := m
	byPos.dedupeCursors(dedupeByPosition)
	if len(byPos.extraCursors) != 0 {
		t.Errorf("dedupeByPosition kept a second cursor at the same position; got %d extras, want 0",
			len(byPos.extraCursors))
	}
}
