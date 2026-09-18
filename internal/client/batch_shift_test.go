package client

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// TestApplyBatchShiftsOverlaysPerOpNotCollapsed is the fourth appearance of the
// collapsed-shift bug: multicursor (Phase 4), undo/redo (Phase 5), the
// updatesMsg handler (Phase 10 item 51), and applyBatch.
//
// The batch here is the shape applyLspEdits and the search-and-replace commit
// both produce — several ops across the file, some adding lines and some
// removing them. Their deltas cancel, so the collapsed version computed a net
// shift of zero and moved nothing, leaving every overlay between the two edit
// points a line out for as long as the cache lived.
//
// The deltas are chosen to cancel deliberately. A batch whose deltas merely
// differ can still pass under the collapsed code by luck, depending on where
// the overlay sits; cancelling makes the collapsed version fail on the
// assertion rather than on the arithmetic.
func TestApplyBatchShiftsOverlaysPerOpNotCollapsed(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	// An overlay on line 4, between the two edit points below.
	m.semanticSpans = highlight.LineSpans{4: []highlight.Span{{StartCol: 0, EndCol: 2, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 4, Col: 0, Label: "hint"}}

	// Ops are applied in the order given. A line is added above the overlay and
	// one removed below it: net zero, but the overlay must still move down one.
	got, _ := applyBatch(m, []document.Op{
		{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "NEW\n"},
		{Type: document.OpDelete, FromLine: 7, FromCol: 0, ToLine: 8, ToCol: 0},
	})

	if _, ok := got.semanticSpans[5]; !ok {
		t.Errorf("semantic spans = %v, want an entry at line 5 — the insert above the "+
			"overlay must shift it even though the batch's net line delta is 0",
			spanLines(got.semanticSpans))
	}
	if len(got.inlayHints) != 1 || got.inlayHints[0].Line != 5 {
		t.Errorf("inlay hint = %v, want line 5", got.inlayHints)
	}
}

func spanLines(m highlight.LineSpans) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestApplyBatchRecordsEveryShiftButOneDestination pins both halves of
// EditRecordMsg at once, because they pull in opposite directions: the shifts
// must be per op, while the jump *destination* must be recorded exactly once —
// a batch is one user action, and emitting one message per op would push a jump
// entry per op and truncate forward history repeatedly.
func TestApplyBatchRecordsEveryShiftButOneDestination(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"

	_, cmd := applyBatch(m, []document.Op{
		{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "NEW\n"},
		{Type: document.OpDelete, FromLine: 7, FromCol: 0, ToLine: 8, ToCol: 0},
	})
	if cmd == nil {
		t.Fatal("applyBatch returned no command")
	}

	var recs []EditRecordMsg
	for _, v := range collectMsgs(sequenceTail(t, cmd)) {
		if r, ok := v.(EditRecordMsg); ok {
			recs = append(recs, r)
		}
	}
	if len(recs) != 1 {
		t.Fatalf("got %d EditRecordMsg, want exactly 1 — a batch is one user action "+
			"and must record one jump destination: %+v", len(recs), recs)
	}
	if got := recs[0].Shifts; len(got) != 2 {
		t.Fatalf("got %d shifts, want 2 (one per line-changing op): %+v", len(got), got)
	}
	if recs[0].Shifts[0] != (LineShift{AtLine: 1, Delta: 1}) {
		t.Errorf("first shift = %+v, want {1 1}", recs[0].Shifts[0])
	}
	if recs[0].Shifts[1] != (LineShift{AtLine: 7, Delta: -1}) {
		t.Errorf("second shift = %+v, want {7 -1}", recs[0].Shifts[1])
	}
}

// sequenceTail returns the last command in a tea.Sequence.
//
// applyBatch returns tea.Sequence(send_1, ..., send_n, tea.Batch(tail...)): the
// sends must reach the server in order, and everything that does not depend on
// send order rides in that final batch, which is where EditRecordMsg is. Only
// the tail is run here — the send commands would dial a zero RPC.
//
// tea.Sequence's message type is unexported, so it is reached by reflection: it
// is a slice of tea.Cmd, which is enough to identify it without naming it.
func sequenceTail(t *testing.T, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	v := reflect.ValueOf(cmd())
	if v.Kind() != reflect.Slice || v.Len() == 0 {
		t.Fatalf("expected a tea.Sequence, got %T", cmd())
	}
	last, ok := v.Index(v.Len() - 1).Interface().(tea.Cmd)
	if !ok {
		t.Fatalf("sequence element is %T, not tea.Cmd", v.Index(v.Len()-1).Interface())
	}
	return last
}

// TestApplyOpRecordsItsSingleShift keeps the one-op path honest through the
// shape change: a lone edit still reports its shift, and a zero-delta edit
// still records a jump destination.
func TestApplyOpRecordsItsSingleShift(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"

	_, cmd := applyOp(m, document.Op{
		Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "NEW\n",
	})
	var rec *EditRecordMsg
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(EditRecordMsg); ok {
			rec = &r
		}
	}
	if rec == nil {
		t.Fatal("no EditRecordMsg from applyOp")
	}
	if len(rec.Shifts) != 1 || rec.Shifts[0] != (LineShift{AtLine: 1, Delta: 1}) {
		t.Errorf("shifts = %+v, want one {1 1}", rec.Shifts)
	}

	// A same-line edit changes no line numbers but is still a jump destination.
	_, cmd = applyOp(m, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	})
	rec = nil
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(EditRecordMsg); ok {
			rec = &r
		}
	}
	if rec == nil {
		t.Fatal("a zero-delta edit must still record a jump destination")
	}
	if len(rec.Shifts) != 1 || rec.Shifts[0].Delta != 0 {
		t.Errorf("shifts = %+v, want one shift with delta 0", rec.Shifts)
	}
}

// TestInsertSessionRecordsPerOpShifts covers executeInsertEsc, which had the
// same collapse in a different shape: one shift derived from the session's net
// line count (m.buf.LineCount() - m.insertLineCount).
func TestInsertSessionRecordsPerOpShifts(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"
	m.mode = ModeInsert
	m.insertLineCount = m.buf.LineCount()
	m.groupBefore = m.cursorSnap()
	// The group holds inverses, in the order the forward edits were made.
	// These are the inverses of: insert a line at 1, then delete a line at 3.
	m.currentGroup = []document.Op{
		{Type: document.OpDelete, FromLine: 1, FromCol: 0, ToLine: 2, ToCol: 0},
		{Type: document.OpInsert, InsertLine: 3, InsertCol: 0, InsertText: "GONE\n"},
	}

	_, cmd := executeInsertEsc(m)
	var rec *EditRecordMsg
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(EditRecordMsg); ok {
			rec = &r
		}
	}
	if rec == nil {
		t.Fatal("no EditRecordMsg when the insert session closed")
	}
	if len(rec.Shifts) != 2 {
		t.Fatalf("got %d shifts, want 2 (one per line-changing op): %+v", len(rec.Shifts), rec.Shifts)
	}
	// Forward deltas: the stored inverses negate, so the delete of 1..2 above
	// was an insert of one line at 1, and the insert at 3 was a delete.
	if rec.Shifts[0] != (LineShift{AtLine: 1, Delta: 1}) {
		t.Errorf("first shift = %+v, want {1 1}", rec.Shifts[0])
	}
	if rec.Shifts[1] != (LineShift{AtLine: 3, Delta: -1}) {
		t.Errorf("second shift = %+v, want {3 -1}", rec.Shifts[1])
	}
}

// TestInsertSessionIgnoresRemoteLineChanges is the second bug in that same
// line, independent of the collapse.
//
// m.insertLineCount is captured when insert mode is entered and never reset,
// but a remote op arriving mid-session closes the undo group (Phase 9 item 45)
// without touching it. So the old net-line-count figure also counted another
// client's line changes as part of this session, and shifted the jump list by
// them. Deriving the shifts from the group's own ops cannot: the group holds
// only this client's edits.
func TestInsertSessionIgnoresRemoteLineChanges(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"
	m.mode = ModeInsert
	// The session began when the buffer had 5 lines; a remote client has since
	// removed two, which has nothing to do with what was typed here.
	m.insertLineCount = m.buf.LineCount() + 2
	m.groupBefore = m.cursorSnap()
	// One typed character: no line-count change at all.
	m.currentGroup = []document.Op{
		{Type: document.OpDelete, FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 1},
	}

	_, cmd := executeInsertEsc(m)
	var rec *EditRecordMsg
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(EditRecordMsg); ok {
			rec = &r
		}
	}
	if rec == nil {
		t.Fatal("no EditRecordMsg when the insert session closed")
	}
	if len(rec.Shifts) != 0 {
		t.Errorf("shifts = %+v, want none — this session changed no line counts, so "+
			"another client's edits must not be attributed to it", rec.Shifts)
	}
}

// TestUndoEmitsPerOpShiftsInApplicationOrder covers the UndoMsg half of the
// collapsed-shift class.
//
// Phase 5 item 18 fixed the *overlay* half of executeUndo — shifting per op
// inside the apply loop — and left the jump-list half summing into one
// (AtLine, LineDelta) in a second loop. Half a function fixed, with nothing
// marking the other half.
//
// That second loop also ran forward over entry.ops while the apply loop runs
// backward. Harmless for a minimum and a sum; the whole of it for an ordered
// list, which is why the shifts are now collected where they are applied.
func TestUndoEmitsPerOpShiftsInApplicationOrder(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"
	// One undo entry holding two ops at different places — the shape applyBatch
	// pushes for an LSP code action or a search-and-replace commit. An entry is
	// undone last-op-first, so ops[1] is applied before ops[0].
	m.undoStack = []undoEntry{{
		ops: []document.Op{
			{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "A\n"},
			{Type: document.OpDelete, FromLine: 5, FromCol: 0, ToLine: 6, ToCol: 0},
		},
		before: cursorSnapshot{cursor: document.Pos{Line: 0, Col: 0}},
	}}

	_, cmd := executeUndo(m)
	var got *UndoMsg
	for _, v := range collectMsgs(sequenceTail(t, cmd)) {
		if u, ok := v.(UndoMsg); ok {
			got = &u
		}
	}
	if got == nil {
		t.Fatal("no UndoMsg from executeUndo")
	}
	if len(got.Shifts) != 2 {
		t.Fatalf("got %d shifts, want 2 (one per line-changing op): %+v", len(got.Shifts), got.Shifts)
	}
	// Application order: the delete at line 5 is applied first.
	if got.Shifts[0] != (LineShift{AtLine: 5, Delta: -1}) {
		t.Errorf("first shift = %+v, want {5 -1} — shifts must be in the order the "+
			"ops were applied, which is the reverse of the entry's order", got.Shifts[0])
	}
	if got.Shifts[1] != (LineShift{AtLine: 1, Delta: 1}) {
		t.Errorf("second shift = %+v, want {1 1}", got.Shifts[1])
	}
}

// TestRedoNotifiesTheApp covers a gap rather than a collapse: executeRedo sent
// the App nothing at all. No UndoMsg, no EditRecordMsg — so a redo that changed
// line counts left every jump entry below it pointing at the wrong line, and
// left entries a redone delete should re-suspend still active.
//
// The shifts are the forward ops, in application order.
func TestRedoNotifiesTheApp(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	m.filePath = "/tmp/x.go"
	// A redo entry holding two ops at different places. Like undo, a redo entry
	// is applied last-op-first.
	m.redoStack = []undoEntry{{
		ops: []document.Op{
			{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "A\n"},
			{Type: document.OpDelete, FromLine: 5, FromCol: 0, ToLine: 6, ToCol: 0},
		},
		before: cursorSnapshot{cursor: document.Pos{Line: 0, Col: 0}},
	}}

	_, cmd := executeRedo(m)
	var got *RedoMsg
	for _, v := range collectMsgs(sequenceTail(t, cmd)) {
		if r, ok := v.(RedoMsg); ok {
			got = &r
		}
	}
	if got == nil {
		t.Fatal("executeRedo told the App nothing — the jump list is never adjusted after a redo")
	}
	if len(got.Shifts) != 2 {
		t.Fatalf("got %d shifts, want 2 (one per line-changing op): %+v", len(got.Shifts), got.Shifts)
	}
	if got.Shifts[0] != (LineShift{AtLine: 5, Delta: -1}) {
		t.Errorf("first shift = %+v, want {5 -1} (application order)", got.Shifts[0])
	}
	if got.Shifts[1] != (LineShift{AtLine: 1, Delta: 1}) {
		t.Errorf("second shift = %+v, want {1 1}", got.Shifts[1])
	}
	// The depth after the redo, so an entry this re-suspends carries the same
	// deactivatedDepth the original edit gave it.
	if got.NewDepth != len(m.undoStack)+1 {
		t.Errorf("NewDepth = %d, want %d (the undo depth after the redo)",
			got.NewDepth, len(m.undoStack)+1)
	}
}
