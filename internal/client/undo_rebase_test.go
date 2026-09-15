package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestUndoAfterRemoteOpAppliesAtTheRightPlace is the end-to-end symptom: type
// something, have a remote edit land in front of it, then undo. Before the
// stacks were rebased, the stored inverse still pointed at where the text used
// to be and the undo deleted the wrong characters.
func TestUndoAfterRemoteOpAppliesAtTheRightPlace(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true

	// Local edit: insert "XY" at column 5, giving "helloXY\n", recorded for undo.
	local := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "XY"}
	inv := inverseOp(m, local)
	m.buf.Apply(local)
	m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: m.cursorSnap()})

	// A remote insert lands *before* it: "R" at column 0.
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)
	if got := m2.buf.Content(); got != "RhelloXY\n" {
		t.Fatalf("after the remote op: %q, want %q", got, "RhelloXY\n")
	}

	// The remote op is itself undoable and sits on top, so undo twice: once to
	// revert the remote insert, once to revert the local one.
	next, _ := executeUndo(m2)
	next2, _ := executeUndo(next.(Model))
	got := next2.(Model).buf.Content()

	if got != "hello\n" {
		t.Errorf("after undoing both edits: %q, want %q — the stored inverse must be rebased past the remote op, "+
			"or it deletes at the position the text used to occupy", got, "hello\n")
	}
}

// TestRebaseUndoHistoryLeavesTheUndoStackAlone pins the rule that is easy to
// get backwards. Remote ops are made undoable — their inverse is pushed as the
// top entry — so undo walks back through the remote op before reaching anything
// beneath it. Those deeper entries are already expressed against the document
// they will meet, and rebasing them too counts the remote op twice.
func TestRebaseUndoHistoryLeavesTheUndoStackAlone(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	stored := document.Op{Type: document.OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 7}
	m.undoStack = []undoEntry{{ops: []document.Op{stored}}}

	m = m.rebaseUndoHistory(document.Op{
		Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R",
	})

	if got := m.undoStack[0].ops[0]; got != stored {
		t.Errorf("undo entry was rebased to %+v; it must be left at %+v, since undo reaches it only "+
			"after the remote op's own inverse has removed the remote op again", got, stored)
	}
}

// TestRebaseUndoHistoryDoesNotMutateSharedBacking is a real hazard for a value
// type: Model copies share the undo stack's backing array, so rebasing in place
// would reach every other copy — including ones captured by in-flight commands.
func TestRebaseUndoHistoryDoesNotMutateSharedBacking(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.undoStack = []undoEntry{{ops: []document.Op{{
		Type: document.OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 7,
	}}}}
	before := m.undoStack[0].ops[0]

	_ = m.rebaseUndoHistory(document.Op{
		Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R",
	})

	if m.undoStack[0].ops[0] != before {
		t.Errorf("the original Model's stack was mutated: %+v became %+v", before, m.undoStack[0].ops[0])
	}
}

// TestRebaseUndoHistoryCoversRedoOnly. currentGroup is deliberately excluded:
// the updatesMsg handler closes an open session into its own entry before a
// remote op is applied, which puts those ops below it in the stack where LIFO
// makes them valid again. Rebasing them too would count the remote op twice.
func TestRebaseUndoHistoryCoversRedoOnly(t *testing.T) {
	del := document.Op{Type: document.OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 7}
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.currentGroup = []document.Op{del}
	m.redoStack = []undoEntry{{ops: []document.Op{del}}}

	m = m.rebaseUndoHistory(document.Op{
		Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R",
	})

	if m.currentGroup[0].FromCol != 5 {
		t.Errorf("currentGroup was rebased (FromCol = %d); it is closed into an entry instead",
			m.currentGroup[0].FromCol)
	}
	if m.redoStack[0].ops[0].FromCol != 6 {
		t.Errorf("redoStack was not rebased: FromCol = %d, want 6", m.redoStack[0].ops[0].FromCol)
	}
}

// TestUndoAfterRemoteOpMidInsertSession covers a remote edit landing while an
// insert session is open. The session is split at that point: what was typed
// before the remote op becomes its own entry beneath it, and what follows
// becomes another above. Undoing back through all three must restore the
// original document, with each step landing where it should.
func TestUndoAfterRemoteOpMidInsertSession(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true

	// Mid-session: "XY" typed at column 5, held in currentGroup.
	local := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "XY"}
	m.currentGroup = []document.Op{inverseOp(m, local)}
	m.buf.Apply(local)

	// A remote insert lands in front of it.
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)
	if got := m2.buf.Content(); got != "RhelloXY\n" {
		t.Fatalf("after the remote op: %q, want %q", got, "RhelloXY\n")
	}
	// The session was closed into its own entry, and a fresh group opened.
	if len(m2.currentGroup) != 0 {
		t.Errorf("currentGroup still holds %d op(s); it should have been closed", len(m2.currentGroup))
	}

	// Undo everything: the remote op's entry, then the session's.
	next := m2
	for len(next.undoStack) > 0 {
		updated, _ := executeUndo(next)
		next = updated.(Model)
	}
	if got := next.buf.Content(); got != "hello\n" {
		t.Errorf("after undoing everything: %q, want %q", got, "hello\n")
	}
}
