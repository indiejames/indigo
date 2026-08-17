package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// TestExecuteUndoShiftsLSPOverlays is a regression test: executeUndo used to
// call only m.reparseHighlight() (tree-sitter syntax highlighting) after
// applying its inverse ops, never shiftLSPOverlayLines/scheduleLSPOverlayRefresh
// (semantic tokens/inlay hints) like every forward-edit path does. Undoing a
// line-count-changing edit left cached LSP overlay data pointing at stale
// (pre-undo) line numbers.
func TestExecuteUndoShiftsLSPOverlays(t *testing.T) {
	// Buffer already reflects the forward edit: "NEW\n" was inserted as
	// line 1, pushing the original "line1"/"line2"/"line3" down by one.
	m := newTestModel("line0\nNEW\nline1\nline2\nline3\n")
	m.rpc = &RPC{}
	// Cached semantic data at line 3 ("line2" in the post-edit buffer).
	m.semanticSpans = highlight.LineSpans{3: []highlight.Span{{StartCol: 0, EndCol: 5, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 3, Col: 0, Label: "hint"}}
	// The undo entry's op is the inverse of the forward insert: delete the
	// "NEW\n" line.
	m.undoStack = []undoEntry{{
		ops: []document.Op{{
			Type:     document.OpDelete,
			FromLine: 1,
			FromCol:  0,
			ToLine:   2,
			ToCol:    0,
		}},
		before: cursorSnapshot{cursor: document.Pos{Line: 0, Col: 0}},
	}}

	m2, _ := executeUndo(m)
	got := m2.(Model)

	if want := "line0\nline1\nline2\nline3\n"; got.buf.Content() != want {
		t.Fatalf("content after undo = %q, want %q", got.buf.Content(), want)
	}
	if _, staleAt3 := got.semanticSpans[3]; staleAt3 {
		t.Error("semanticSpans still has a stale entry at line 3 — not shifted")
	}
	if spans, ok := got.semanticSpans[2]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans[2] = %v, want the span shifted up from line 3", got.semanticSpans[2])
	}
	if len(got.inlayHints) != 1 || got.inlayHints[0].Line != 2 {
		t.Errorf("inlayHints = %+v, want a single hint shifted to line 2", got.inlayHints)
	}
	if got.lspOverlaySeq != m.lspOverlaySeq+1 {
		t.Errorf("lspOverlaySeq = %d, want %d (scheduleLSPOverlayRefresh never called)", got.lspOverlaySeq, m.lspOverlaySeq+1)
	}
}

// TestExecuteYankNoSelectionCopiesCharUnderCursor is a regression test:
// executeYank used to be a no-op when m.sel was nil, requiring an explicit
// selection before `y` did anything. It should fall back to yanking the
// single character under the cursor, matching how `d`/`c` already act on the
// character under the cursor when nothing is selected.
func TestExecuteYankNoSelectionCopiesCharUnderCursor(t *testing.T) {
	prev, prevErr := readClipboard()
	if err := writeClipboard("sentinel"); err != nil {
		t.Skipf("no clipboard tool available: %v", err)
	}
	if prevErr == nil {
		t.Cleanup(func() {
			if err := writeClipboard(prev); err != nil {
				t.Logf("failed to restore clipboard: %v", err)
			}
		})
	}

	m := newTestModel("hello\nworld\n")
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.sel = nil

	m2, _ := executeYank(m)
	got := m2.(Model)

	text, err := readClipboard()
	if err != nil {
		t.Fatalf("readClipboard: %v", err)
	}
	if text != "e" {
		t.Errorf("clipboard = %q, want %q", text, "e")
	}
	if got.sel != nil {
		t.Error("executeYank should leave sel nil when there was no selection")
	}
}

// TestExecuteRedoShiftsLSPOverlays is the redo counterpart of
// TestExecuteUndoShiftsLSPOverlays.
func TestExecuteRedoShiftsLSPOverlays(t *testing.T) {
	// Buffer reflects the pre-redo (undone) state.
	m := newTestModel("line0\nline1\nline2\nline3\n")
	m.rpc = &RPC{}
	// Cached semantic data at line 2 ("line2" in the pre-redo buffer).
	m.semanticSpans = highlight.LineSpans{2: []highlight.Span{{StartCol: 0, EndCol: 5, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 2, Col: 0, Label: "hint"}}
	// The redo entry's op is the original forward insert: add "NEW\n" as
	// line 1.
	m.redoStack = []undoEntry{{
		ops: []document.Op{{
			Type:       document.OpInsert,
			InsertLine: 1,
			InsertCol:  0,
			InsertText: "NEW\n",
		}},
		before: cursorSnapshot{cursor: document.Pos{Line: 0, Col: 0}},
	}}

	m2, _ := executeRedo(m)
	got := m2.(Model)

	if want := "line0\nNEW\nline1\nline2\nline3\n"; got.buf.Content() != want {
		t.Fatalf("content after redo = %q, want %q", got.buf.Content(), want)
	}
	if _, staleAt2 := got.semanticSpans[2]; staleAt2 {
		t.Error("semanticSpans still has a stale entry at line 2 — not shifted")
	}
	if spans, ok := got.semanticSpans[3]; !ok || len(spans) != 1 {
		t.Errorf("semanticSpans[3] = %v, want the span shifted down from line 2", got.semanticSpans[3])
	}
	if len(got.inlayHints) != 1 || got.inlayHints[0].Line != 3 {
		t.Errorf("inlayHints = %+v, want a single hint shifted to line 3", got.inlayHints)
	}
	if got.lspOverlaySeq != m.lspOverlaySeq+1 {
		t.Errorf("lspOverlaySeq = %d, want %d (scheduleLSPOverlayRefresh never called)", got.lspOverlaySeq, m.lspOverlaySeq+1)
	}
}
