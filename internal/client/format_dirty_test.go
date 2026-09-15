package client

import (
	"testing"
)

// TestFormatMarksBufferDirty is a regression test for silent data loss. The
// server marks its swapped-in buffer dirty after a format that changed
// something (Format's newBuf.MarkDirty(), server_lsp.go), but the client's
// formatResultMsg handler replaced m.buf with a fresh document.New — which is
// clean. Via doSave (thenSave: true) that was masked by the save that follows,
// but ":format" passes thenSave: false, so formatting then ":q" exited with no
// unsaved-changes prompt and threw the reformatted content away.
func TestFormatMarksBufferDirty(t *testing.T) {
	m := newTestModel("package main\nfunc  main(){}\n")
	m.rpc = &RPC{}

	updated, _ := m.Update(formatResultMsg{
		bufID:      m.bufID,
		content:    "package main\n\nfunc main() {}\n",
		changed:    true,
		generation: 3,
	})
	m2 := updated.(Model)

	if m2.buf.Content() != "package main\n\nfunc main() {}\n" {
		t.Fatalf("buf.Content() = %q, want the formatted content", m2.buf.Content())
	}
	if !m2.Dirty() {
		t.Error("Dirty() = false after a format that changed the buffer; " +
			"the server marks its copy dirty, so :format then :q would discard the formatting with no prompt")
	}
}

// TestFormatSavedUndoDepthCannotFalselyClean covers the second half of the
// same fix. savedUndoDepth is compared against len(undoStack) to re-clear the
// dirty marker when the user undoes back to the last saved state; leaving it
// at 0 against a just-emptied undo stack meant one edit-then-undo after a
// format wrongly reported the formatted-but-unsaved buffer as saved.
func TestFormatSavedUndoDepthCannotFalselyClean(t *testing.T) {
	m := newTestModel("package main\n")
	m.rpc = &RPC{}

	updated, _ := m.Update(formatResultMsg{
		bufID:      m.bufID,
		content:    "package main\n\n",
		changed:    true,
		generation: 1,
	})
	m2 := updated.(Model)

	if len(m2.undoStack) != 0 {
		t.Fatalf("test setup: undoStack has %d entries, want 0 after a format", len(m2.undoStack))
	}
	if m2.savedUndoDepth == len(m2.undoStack) {
		t.Errorf("savedUndoDepth = %d matches len(undoStack) = %d, so undoing back to this point "+
			"would mark a formatted-but-unsaved buffer clean", m2.savedUndoDepth, len(m2.undoStack))
	}
}

// TestFormatThenSaveStillEndsClean guards the other direction: marking dirty
// on the format must not leave a format-on-save permanently dirty. doSaveNow
// captures m.buf.Version() (0 straight after the format's fresh buffer), which
// still matches when savedMsg's staleness check runs, so SetClean is reached.
func TestFormatThenSaveStillEndsClean(t *testing.T) {
	m := newTestModel("package main\n")
	m.rpc = &RPC{}

	updated, _ := m.Update(formatResultMsg{
		bufID:      m.bufID,
		content:    "package main\n\n",
		changed:    true,
		thenSave:   true,
		generation: 1,
	})
	m2 := updated.(Model)
	if !m2.Dirty() {
		t.Fatal("test setup: buffer should be dirty immediately after the format swap")
	}

	// What doSaveNow stamps on the request, and what the server would confirm.
	updated2, _ := m2.Update(savedMsg{bufID: m2.bufID, version: m2.buf.Version()})
	m3 := updated2.(Model)

	if m3.Dirty() {
		t.Error("Dirty() = true after the save following a format-on-save; the save must still clear it")
	}
}
