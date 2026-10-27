package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// editRecords runs cmd with the send queue paused, so the edits' sends stay
// queued instead of dialling the test's zero RPC, and returns every
// EditRecordMsg it produced.
func editRecords(cmd tea.Cmd) []EditRecordMsg {
	var recs []EditRecordMsg
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(EditRecordMsg); ok {
			recs = append(recs, r)
		}
	}
	return recs
}

func pausedModel(content string) Model {
	m := newTestModel(content)
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"
	m.sendQ.paused = true
	return m
}

// TestMultiCursorDeleteTellsTheJumpList is a regression test.
// deleteAllCursorSelections opens its own undo group and commits it itself, and
// applyOp records an edit only when no group is open, so a normal-mode
// multi-cursor delete sent no EditRecordMsg: every jump entry below the deleted
// lines kept pointing at its old line number, and the delete recorded no jump
// destination. It must send one message for the one action, with one shift per
// line it removed, in the order the deletes were applied.
func TestMultiCursorDeleteTellsTheJumpList(t *testing.T) {
	m := pausedModel("l0\nl1\nl2\nl3\nl4\n")
	// Each cursor selects a whole line up to and including its line break.
	m.cursor = document.Pos{Line: 1, Col: 2}
	m.sel = &Selection{Anchor: document.Pos{Line: 1, Col: 0}, Head: document.Pos{Line: 2, Col: 0}}
	m.extraCursors = []ExtraCursor{{
		pos:     document.Pos{Line: 3, Col: 2},
		sel:     &Selection{Anchor: document.Pos{Line: 3, Col: 0}, Head: document.Pos{Line: 4, Col: 0}},
		goalCol: -1,
	}}

	got, cmd := deleteAllCursorSelections(m, false)
	if s := got.buf.Content(); s != "l0\nl2\nl4\n" {
		t.Fatalf("buffer = %q, want both lines deleted", s)
	}
	recs := editRecords(cmd)
	if len(recs) != 1 {
		t.Fatalf("got %d EditRecordMsg, want 1 for one multi-cursor delete: %+v", len(recs), recs)
	}
	r := recs[0]
	// Deleted back to front, so line 3 first, then line 1.
	want := []LineShift{{AtLine: 3, Delta: -1}, {AtLine: 1, Delta: -1}}
	if len(r.Shifts) != len(want) || r.Shifts[0] != want[0] || r.Shifts[1] != want[1] {
		t.Errorf("shifts = %+v, want %+v", r.Shifts, want)
	}
	if r.Line != 1 || r.FilePath != "/tmp/x.go" {
		t.Errorf("destination = %s:%d, want /tmp/x.go:1, the primary cursor's", r.FilePath, r.Line)
	}
	if r.UndoDepth != len(got.undoStack) {
		t.Errorf("UndoDepth = %d, want %d, the depth of the group just committed", r.UndoDepth, len(got.undoStack))
	}
}

// TestMultiCursorCaseConvertTellsTheJumpList is the same gap in
// convertCaseAllCursors, which opens and commits its own group the same way.
func TestMultiCursorCaseConvertTellsTheJumpList(t *testing.T) {
	m := pausedModel("fooBar\nx\nbazQux\n")
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 2, Col: 1}, goalCol: -1}}

	got, cmd := convertCaseAllCursors(m, joinSnakeCase)
	if s := got.buf.Content(); s != "foo_bar\nx\nbaz_qux\n" {
		t.Fatalf("buffer = %q, want both identifiers converted", s)
	}
	recs := editRecords(cmd)
	if len(recs) != 1 {
		t.Fatalf("got %d EditRecordMsg, want 1 for one multi-cursor case conversion: %+v", len(recs), recs)
	}
	if recs[0].Line != 0 || len(recs[0].Shifts) != 0 {
		t.Errorf("record = %+v, want destination line 0 and no shifts, since no line count changed", recs[0])
	}
}

// TestMultiCursorDeleteInsideAGroupRecordsNothing keeps the other half: a
// delete inside an already-open group (an insert session) belongs to that
// group, whose own commit records it, so recording it here as well would push a
// second jump entry for one action.
func TestMultiCursorDeleteInsideAGroupRecordsNothing(t *testing.T) {
	m := pausedModel("l0\nl1\nl2\n")
	m.currentGroup = []document.Op{}
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 2, Col: 0}, goalCol: -1}}

	_, cmd := deleteAllCursorSelections(m, false)
	if recs := editRecords(cmd); len(recs) != 0 {
		t.Errorf("got %d EditRecordMsg inside an open group, want 0: %+v", len(recs), recs)
	}
}
