package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// TestCursorFollowsRemoteEdit: a remote insert *before* the cursor must move the
// cursor with it. Nothing else does — the updatesMsg handler only clamps to
// bounds — so without this the caret silently comes to point at different text
// than it did, and the next keystroke lands somewhere the user did not choose.
func TestCursorFollowsRemoteEdit(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.cursor = document.Pos{Line: 0, Col: 5} // at the end of "hello"

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XY"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if got := m2.buf.Content(); got != "XYhello\n" {
		t.Fatalf("content = %q, want %q", got, "XYhello\n")
	}
	if m2.cursor.Col != 7 {
		t.Errorf("cursor col = %d, want 7 — two runes were inserted before it, so it must move with the text "+
			"it was sitting after", m2.cursor.Col)
	}
}

// TestCursorFollowsRemoteEditAcrossLines covers the line axis: an insert that
// adds a line above the cursor must push it down.
func TestCursorFollowsRemoteEditAcrossLines(t *testing.T) {
	m := newTestModel("one\ntwo\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.cursor = document.Pos{Line: 1, Col: 2}

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "new\n"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if m2.cursor.Line != 2 || m2.cursor.Col != 2 {
		t.Errorf("cursor = %d:%d, want 2:2 — a line was inserted above it", m2.cursor.Line, m2.cursor.Col)
	}
}

// TestUndoSnapshotIsNotShifted pins the other half of the rule. An entry's
// caret snapshot is restored only after every entry above it has been undone —
// including the one recorded for this remote op — so by then the document is
// back to the state that snapshot described. Shifting it as well counts the
// remote op twice and leaves the caret an edit's worth off after each undo.
func TestUndoSnapshotIsNotShifted(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.undoStack = []undoEntry{{
		ops:    []document.Op{{Type: document.OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 6}},
		before: cursorSnapshot{cursor: document.Pos{Line: 0, Col: 5}},
	}}

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XY"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if got := m2.undoStack[0].before.cursor.Col; got != 5 {
		t.Errorf("stored cursor col = %d, want it left at 5", got)
	}
}

// collectMsgs runs a command, unwrapping tea.Batch, and returns every message
// it produces. Used to assert on a message emitted alongside several others.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// TestRemoteEditMsgIsEmittedForLineCountChanges covers the wiring, not the
// handler. internal/app's test feeds RemoteEditMsg in directly, so it passes
// just as happily if nothing ever sends one — and a jump list that silently
// stops being adjusted produces no error, only a jump that lands in the wrong
// place much later.
func TestRemoteEditMsgIsEmittedForLineCountChanges(t *testing.T) {
	m := newTestModel("one\ntwo\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/a.go"
	m.generation = 1
	m.generationKnown = true

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "new\n"}
	_, cmd := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})

	var got *RemoteEditMsg
	for _, msg := range collectMsgs(cmd) {
		if r, ok := msg.(RemoteEditMsg); ok {
			got = &r
		}
	}
	if got == nil {
		t.Fatal("no RemoteEditMsg emitted for a remote edit that added a line")
	}
	if got.FilePath != "/tmp/a.go" || got.LineDelta != 1 || got.AtLine != 0 {
		t.Errorf("RemoteEditMsg = %+v, want {/tmp/a.go, AtLine 0, LineDelta 1}", *got)
	}
}

// TestRemoteEditMsgNotEmittedWithoutLineChange: an edit within one line moves no
// jump entry, so there is nothing to tell the App about.
func TestRemoteEditMsgNotEmittedWithoutLineChange(t *testing.T) {
	m := newTestModel("one\ntwo\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/a.go"
	m.generation = 1
	m.generationKnown = true

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}
	_, cmd := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})

	for _, msg := range collectMsgs(cmd) {
		if _, ok := msg.(RemoteEditMsg); ok {
			t.Error("RemoteEditMsg emitted for a single-line edit, which shifts nothing")
		}
	}
}
