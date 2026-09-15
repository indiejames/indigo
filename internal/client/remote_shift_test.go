package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// TestRemoteOpsShiftOverlaysPerOpNotCollapsed is a regression test for the
// updatesMsg handler collapsing every op in one poll response into a single
// (minimum line, net delta) shift.
//
// Two remote ops whose line deltas cancel — one inserting a line near the top,
// one deleting a line further down — sum to a net delta of zero, so the
// collapsed version shifted nothing at all. An overlay sitting between the two
// edit points is moved by the first and untouched by the second, so it must end
// up one line lower; collapsing left it where it was, pointing at the wrong
// line for as long as the cache lived.
//
// This is the same bug the Phase 4 review found in multicursor and Phase 5
// found in undo/redo. The remote-op path is the third place it appeared.
func TestRemoteOpsShiftOverlaysPerOpNotCollapsed(t *testing.T) {
	// 8 lines. An overlay sits on line 4, between the two edit points.
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	m.bufID = 1
	m.generationKnown = true
	m.semanticSpans = highlight.LineSpans{4: []highlight.Span{{StartCol: 0, EndCol: 2, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 4, Col: 0, Label: "hint"}}

	msg := updatesMsg{
		bufID:   1,
		version: 2,
		ops: []document.Op{
			// +1 line at line 1: everything at or below line 1 moves down.
			{Version: 1, ClientID: 2, Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "NEW\n"},
			// -1 line at line 7 (line 6 before the insert above): below the
			// overlay, so it must not move it.
			{Version: 2, ClientID: 2, Type: document.OpDelete, FromLine: 7, FromCol: 0, ToLine: 8, ToCol: 0},
		},
	}

	updated, _ := m.Update(msg)
	got := updated.(Model)

	// The overlay was on line 4 and one line was inserted above it, so it
	// belongs on line 5.
	if _, ok := got.semanticSpans[5]; !ok {
		t.Errorf("semantic spans = %v, want an entry at line 5 — the insert above "+
			"the overlay must shift it even though the response's net line delta is 0",
			keysOf(got.semanticSpans))
	}
	if len(got.inlayHints) != 1 || got.inlayHints[0].Line != 5 {
		t.Errorf("inlay hint line = %v, want 5", got.inlayHints)
	}
}

func keysOf(m highlight.LineSpans) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRemoteOpsEmitOneRemoteEditPerOp checks the jump list is told about each
// remote op separately. It has the same problem a collapsed overlay shift does:
// a jump entry between two edit points is moved by one op and not the other,
// which a single summed message cannot express.
func TestRemoteOpsEmitOneRemoteEditPerOp(t *testing.T) {
	m := newTestModel("l0\nl1\nl2\nl3\nl4\nl5\nl6\nl7\n")
	m.rpc = &RPC{}
	m.bufID = 1
	m.filePath = "/tmp/x.go"
	m.generationKnown = true

	msg := updatesMsg{
		bufID:   1,
		version: 2,
		ops: []document.Op{
			{Version: 1, ClientID: 2, Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "NEW\n"},
			{Version: 2, ClientID: 2, Type: document.OpDelete, FromLine: 7, FromCol: 0, ToLine: 8, ToCol: 0},
		},
	}

	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("Update returned no command")
	}

	var edits []RemoteEditMsg
	for _, v := range collectMsgs(cmd) {
		if r, ok := v.(RemoteEditMsg); ok {
			edits = append(edits, r)
		}
	}

	if len(edits) != 2 {
		t.Fatalf("got %d RemoteEditMsg, want 2 (one per line-changing op): %+v", len(edits), edits)
	}
	if edits[0].AtLine != 1 || edits[0].LineDelta != 1 {
		t.Errorf("first edit = {AtLine:%d LineDelta:%d}, want {1 1}", edits[0].AtLine, edits[0].LineDelta)
	}
	if edits[1].AtLine != 7 || edits[1].LineDelta != -1 {
		t.Errorf("second edit = {AtLine:%d LineDelta:%d}, want {7 -1}", edits[1].AtLine, edits[1].LineDelta)
	}
}

// TestSearchIdxKeepsColumnAcrossRemoteEdit covers refreshSearchMatches
// resolving the previously-selected match by line alone: with several matches
// on one line that always snapped the selection back to the first of them.
func TestSearchIdxKeepsColumnAcrossRemoteEdit(t *testing.T) {
	m := newTestModel("foo foo foo\nbar\n")
	m.searchQuery = "foo"
	m.updateSearch()
	if len(m.searchMatches) != 3 {
		t.Fatalf("test setup: %d matches, want 3", len(m.searchMatches))
	}
	m.searchIdx = 2 // the third "foo" on line 0
	wantCol := m.searchMatches[2].col

	// A remote edit elsewhere in the buffer; the matches themselves do not move.
	m.refreshSearchMatches()

	if m.searchIdx != 2 {
		t.Fatalf("searchIdx = %d, want 2 — resolving by line alone snaps back to "+
			"the first match on the line", m.searchIdx)
	}
	if got := m.searchMatches[m.searchIdx].col; got != wantCol {
		t.Errorf("selected match col = %d, want %d", got, wantCol)
	}
}
