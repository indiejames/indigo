package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// Regression test: ops arriving from other clients (e.g. an agent adding a
// comment via apply_edits) must be undoable locally. Previously updatesMsg
// applied remote ops without recording anything on the undo stack, so `u`
// silently skipped them.
func TestRemoteOpsAreUndoable(t *testing.T) {
	m := newTestModel("line1\nline2\n")

	op := document.Op{
		Type:       document.OpInsert,
		InsertLine: 0,
		InsertCol:  0,
		InsertText: "// added by agent\n",
		ClientID:   42,
	}
	m2, _ := m.Update(updatesMsg{ops: []document.Op{op}, version: 7})
	got := m2.(Model)

	if want := "// added by agent\nline1\nline2\n"; got.buf.Content() != want {
		t.Fatalf("content after remote op = %q, want %q", got.buf.Content(), want)
	}
	if got.version != 7 {
		t.Errorf("version = %d, want 7", got.version)
	}
	if len(got.undoStack) != 1 {
		t.Fatalf("undoStack len = %d, want 1", len(got.undoStack))
	}

	// Apply the recorded inverses in reverse order, exactly as the `u` key
	// handler does, and verify the buffer returns to its original content.
	entry := got.undoStack[0]
	for i := len(entry.ops) - 1; i >= 0; i-- {
		got.buf.Apply(entry.ops[i])
	}
	if want := "line1\nline2\n"; got.buf.Content() != want {
		t.Errorf("content after undo = %q, want %q", got.buf.Content(), want)
	}
}

// A remote delete+insert pair (the shape apply_edits produces) arriving in one
// updatesMsg batch must record a single undo entry that reverts both ops.
func TestRemoteOpBatchRecordsSingleUndoEntry(t *testing.T) {
	m := newTestModel("hello world\n")

	ops := []document.Op{
		{Type: document.OpDelete, FromLine: 0, FromCol: 6, ToLine: 0, ToCol: 11, ClientID: 42},
		{Type: document.OpInsert, InsertLine: 0, InsertCol: 6, InsertText: "editor", ClientID: 42},
	}
	m2, _ := m.Update(updatesMsg{ops: ops, version: 2})
	got := m2.(Model)

	if want := "hello editor\n"; got.buf.Content() != want {
		t.Fatalf("content after remote batch = %q, want %q", got.buf.Content(), want)
	}
	if len(got.undoStack) != 1 {
		t.Fatalf("undoStack len = %d, want 1 (batch must be one undo entry)", len(got.undoStack))
	}

	entry := got.undoStack[0]
	for i := len(entry.ops) - 1; i >= 0; i-- {
		got.buf.Apply(entry.ops[i])
	}
	if want := "hello world\n"; got.buf.Content() != want {
		t.Errorf("content after undo = %q, want %q", got.buf.Content(), want)
	}
}

// selEqual/copySel drive selection-change reporting to the server; copySel
// must snapshot (handlers mutate the pointed-to Selection in place) and
// selEqual must treat nil and equal-range selections correctly.
func TestSelEqualAndCopySel(t *testing.T) {
	a := &Selection{Anchor: document.Pos{Line: 1, Col: 2}, Head: document.Pos{Line: 3, Col: 4}}

	if !selEqual(nil, nil) {
		t.Error("selEqual(nil, nil) = false")
	}
	if selEqual(a, nil) || selEqual(nil, a) {
		t.Error("selEqual(sel, nil) = true")
	}

	snap := copySel(a)
	if !selEqual(a, snap) {
		t.Error("copy not equal to original")
	}
	// In-place mutation (as visual-mode handlers do) must not affect the copy.
	a.Head = document.Pos{Line: 9, Col: 0}
	if selEqual(a, snap) {
		t.Error("copySel did not snapshot: mutation leaked into the copy")
	}
	if copySel(nil) != nil {
		t.Error("copySel(nil) != nil")
	}
}

// Remote ops must clear the redo stack, like any other new edit.
func TestRemoteOpsClearRedoStack(t *testing.T) {
	m := newTestModel("line1\n")
	m.redoStack = []undoEntry{{ops: []document.Op{{Type: document.OpNoop}}}}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x", ClientID: 42}
	m2, _ := m.Update(updatesMsg{ops: []document.Op{op}, version: 1})
	got := m2.(Model)

	if len(got.redoStack) != 0 {
		t.Errorf("redoStack len = %d, want 0 after remote edit", len(got.redoStack))
	}
}
