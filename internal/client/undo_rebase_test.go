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

// TestRebaseUndoHistoryCoversRedoAndCurrentGroup: all three holders of stored
// coordinates need rebasing, and missing any one of them is a silent wrong-place
// edit later rather than an error.
func TestRebaseUndoHistoryCoversRedoAndCurrentGroup(t *testing.T) {
	del := document.Op{Type: document.OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 7}
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.currentGroup = []document.Op{del}
	m.redoStack = []undoEntry{{ops: []document.Op{del}}}

	m = m.rebaseUndoHistory(document.Op{
		Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "R",
	})

	if m.currentGroup[0].FromCol != 6 {
		t.Errorf("currentGroup was not rebased: FromCol = %d, want 6", m.currentGroup[0].FromCol)
	}
	if m.redoStack[0].ops[0].FromCol != 6 {
		t.Errorf("redoStack was not rebased: FromCol = %d, want 6", m.redoStack[0].ops[0].FromCol)
	}
}

// TestUndoAfterRemoteOpMidInsertSession is the case currentGroup exists for: a
// remote edit lands while an insert session is open, so the session's inverses
// end up *above* the remote op's entry and are applied to a document that still
// contains it.
func TestUndoAfterRemoteOpMidInsertSession(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true

	// Mid-session: "XY" typed at column 5, its inverse held in currentGroup
	// rather than pushed, exactly as insert mode does.
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

	// End the session: the group becomes the top undo entry.
	m2.undoStack = append(m2.undoStack, undoEntry{ops: m2.currentGroup, before: m2.cursorSnap()})
	m2.currentGroup = nil

	next, _ := executeUndo(m2)
	if got := next.(Model).buf.Content(); got != "Rhello\n" {
		t.Errorf("after undoing the insert session: %q, want %q — the group's inverse must be rebased past "+
			"the remote op, since it is applied to a document that still contains it", got, "Rhello\n")
	}
}
