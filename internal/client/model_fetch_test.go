package client

import (
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestCodeActionRangeNoSelectionIsZeroWidthAtCursor(t *testing.T) {
	cursor := document.Pos{Line: 3, Col: 7}
	sl, sc, el, ec := codeActionRange(cursor, nil)
	if sl != 3 || sc != 7 || el != 3 || ec != 7 {
		t.Errorf("codeActionRange(cursor, nil) = (%d,%d,%d,%d), want (3,7,3,7)", sl, sc, el, ec)
	}
}

func TestCodeActionRangeUsesSelectionOrderedWithExclusiveEnd(t *testing.T) {
	cursor := document.Pos{Line: 0, Col: 0}
	sel := &Selection{Anchor: document.Pos{Line: 2, Col: 4}, Head: document.Pos{Line: 5, Col: 9}}

	sl, sc, el, ec := codeActionRange(cursor, sel)
	if sl != 2 || sc != 4 || el != 5 || ec != 10 {
		t.Errorf("codeActionRange = (%d,%d,%d,%d), want (2,4,5,10) — end col should be exclusive (Head.Col+1)", sl, sc, el, ec)
	}
}

func TestCodeActionRangeSelectionReversedStillOrdered(t *testing.T) {
	// Anchor after Head: selection made by moving backward from where it started.
	cursor := document.Pos{Line: 0, Col: 0}
	sel := &Selection{Anchor: document.Pos{Line: 5, Col: 9}, Head: document.Pos{Line: 2, Col: 4}}

	sl, sc, el, ec := codeActionRange(cursor, sel)
	if sl != 2 || sc != 4 || el != 5 || ec != 10 {
		t.Errorf("codeActionRange = (%d,%d,%d,%d), want (2,4,5,10) regardless of anchor/head order", sl, sc, el, ec)
	}
}

func TestResolveDestPathRelative(t *testing.T) {
	got, err := resolveDestPath("/work/proj", "sub/helpers.go")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/work/proj", "sub/helpers.go")
	if got != want {
		t.Errorf("resolveDestPath = %q, want %q", got, want)
	}
}

func TestResolveDestPathAbsolute(t *testing.T) {
	got, err := resolveDestPath("/work/proj", "/elsewhere/helpers.go")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/elsewhere/helpers.go" {
		t.Errorf("resolveDestPath = %q, want %q", got, "/elsewhere/helpers.go")
	}
}

// TestApplyLspEditsIsUndoableAndMarksDirty is a regression test: applying an
// LSP code action (e.g. Extract Function) used to send raw ops to the server
// and rebuild the buffer wholesale via document.New, which left the buffer
// unmarked-dirty and wiped the undo stack. Applying through applyLspEdits
// must behave like any normal edit: dirty flag set, one undo entry whose
// inverse ops restore the original content exactly.
func TestApplyLspEditsIsUndoableAndMarksDirty(t *testing.T) {
	m := newTestModel("x := 1\ny := 2\nz := x + y\n")

	// Replace lines 0-2 with a call, like an extract-function edit would.
	edits := []ClientLspEdit{
		{FromLine: 0, FromCol: 0, ToLine: 2, ToCol: 10, NewText: "z := newFunction()"},
	}
	m2, _ := applyLspEdits(m, edits)

	if got := m2.buf.Content(); got != "z := newFunction()\n" {
		t.Fatalf("content after edit = %q, want %q", got, "z := newFunction()\n")
	}
	if !m2.buf.Dirty() {
		t.Error("buffer should be marked dirty after applying an LSP edit")
	}
	if len(m2.undoStack) != 1 {
		t.Fatalf("undoStack len = %d, want 1 (edit must be undoable)", len(m2.undoStack))
	}

	// Replay the inverse ops in reverse order, exactly like the undo handler.
	entry := m2.undoStack[0]
	for i := len(entry.ops) - 1; i >= 0; i-- {
		m2.buf.Apply(entry.ops[i])
	}
	if got := m2.buf.Content(); got != "x := 1\ny := 2\nz := x + y\n" {
		t.Errorf("content after undo = %q, want the original", got)
	}
}

// TestApplyLspEditsMultipleEdits checks that multiple edits apply in reverse
// document order so earlier edits don't shift later positions.
func TestApplyLspEditsMultipleEdits(t *testing.T) {
	m := newTestModel("aaa\nbbb\nccc\n")

	edits := []ClientLspEdit{
		{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 3, NewText: "AAA"},
		{FromLine: 2, FromCol: 0, ToLine: 2, ToCol: 3, NewText: "CCC"},
	}
	m2, _ := applyLspEdits(m, edits)

	if got := m2.buf.Content(); got != "AAA\nbbb\nCCC\n" {
		t.Errorf("content = %q, want %q", got, "AAA\nbbb\nCCC\n")
	}
}
