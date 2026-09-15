package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func opIns(line, col int, text string) document.Op {
	return document.Op{Type: document.OpInsert, InsertLine: line, InsertCol: col, InsertText: text}
}

// TestDropAckedBySeqRetiresBySendOrder covers the one source of truth for
// acknowledgement. seq is the op's index in this client's send order and the
// server counts ops from each client in that same order, so the first
// appliedFromCaller entries are exactly the ones it has processed.
func TestDropAckedBySeqRetiresBySendOrder(t *testing.T) {
	pending := []pendingOp{{seq: 1}, {seq: 2}, {seq: 3}}

	got := dropAckedBySeq(pending, 2)
	if len(got) != 1 || got[0].seq != 3 {
		t.Errorf("dropAckedBySeq(_, 2) = %+v, want only seq 3 outstanding", got)
	}
	if got := dropAckedBySeq(pending, 0); len(got) != 3 {
		t.Errorf("nothing applied yet, but %d of 3 entries were retired", 3-len(got))
	}
}

// TestRebasePastPendingRebasesEverythingOutstanding: after dropAckedBySeq has
// run, every remaining entry is one the delivered ops do not account for, so all
// of them are rebased past — no further filtering.
func TestRebasePastPendingRebasesConcurrentOps(t *testing.T) {
	pending := []pendingOp{{seq: 1, ops: []document.Op{opIns(0, 0, "AA")}}}
	remote := document.Op{Version: 5, Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "R"}

	updated, rebased := rebasePastPending(pending, remote)
	if len(rebased) != 1 || rebased[0].InsertCol != 4 {
		t.Errorf("rebased = %+v, want a single insert at column 4 (shifted past the in-flight \"AA\")", rebased)
	}
	// The pending op is rewritten to account for the remote op too, so the next
	// incoming op is measured against the right thing.
	if updated[0].ops[0].InsertCol != 0 {
		t.Errorf("pending op moved to column %d; it is before the remote op and should not have", updated[0].ops[0].InsertCol)
	}
}

// TestUpdatesMsgRebasesRemoteOpPastPending covers the wiring, not just the
// helper. A helper that is correct but never called produces silent divergence
// with no error, which is exactly the failure this project exists to remove.
func TestUpdatesMsgRebasesRemoteOpPastPending(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.version = 0

	// This client has typed "AA" at the start and it is still in flight, so the
	// local buffer is "AAhello\n" while the server still shows "hello\n".
	local := opIns(0, 0, "AA")
	m.buf.Apply(local)
	m.pending = []pendingOp{{seq: 1, ops: []document.Op{local}}}

	// A remote op the server computed against "hello\n": insert "R" after "he".
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "R"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	// Applied verbatim it would land at column 2 of "AAhello\n" — inside "AA" —
	// giving "AARhello\n".
	if got := m2.buf.Content(); got != "AAheRllo\n" {
		t.Errorf("buf.Content() = %q, want %q — the remote op must be rebased past this client's in-flight edit",
			got, "AAheRllo\n")
	}
}

// TestUpdatesMsgRetiresPendingBeforeRebasing is the ordering property that makes
// the single source of truth work: entries the poll reports as applied are
// retired *before* anything is rebased past them, because the ops delivered in
// that same response already account for them.
func TestUpdatesMsgRetiresPendingBeforeRebasing(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.nextSeq = 2

	local := opIns(0, 0, "AA")
	m.buf.Apply(local)
	m.pending = []pendingOp{
		{seq: 1, ops: []document.Op{local}},
		{seq: 2, ops: []document.Op{opIns(0, 9, "B")}},
	}

	// The server reports it has applied the first, and delivers a remote op
	// already rewritten past it.
	remote := document.Op{Version: 3, Type: document.OpInsert, InsertLine: 0, InsertCol: 4, InsertText: "R"}
	updated, _ := m.Update(updatesMsg{
		bufID: m.bufID, ops: []document.Op{remote}, version: 3, generation: 1,
		appliedFromCaller: 1,
	})
	m2 := updated.(Model)

	if len(m2.pending) != 1 || m2.pending[0].seq != 2 {
		t.Fatalf("pending = %+v, want only seq 2 outstanding", m2.pending)
	}
	// seq 2 inserts at column 9, past the remote op, so the remote op keeps its
	// column. Had seq 1 still been counted, it would have been pushed to 6.
	if got := m2.buf.Content(); got != "AAheRllo\n" {
		t.Errorf("buf.Content() = %q, want %q — a retired op must not be rebased past a second time",
			got, "AAheRllo\n")
	}
}

// TestResyncResetsSeqNumbering pairs with the server clearing its per-client
// counts on a buffer swap. That count is what retires pending entries, so if
// seq numbering carried on from where it was, every entry after a swap would
// look unacknowledged forever and incoming ops would be rebased past edits the
// server had long since applied.
func TestResyncResetsSeqNumbering(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.nextSeq = 42
	m.pending = []pendingOp{{seq: 42, ops: []document.Op{opIns(0, 0, "A")}}}

	updated, _ := m.Update(bufferResyncMsg{
		bufID: m.bufID, content: "fresh\n", version: 0, generation: 3, path: m.filePath,
	})
	m2 := updated.(Model)

	if len(m2.pending) != 0 {
		t.Errorf("pending = %+v, want empty after a resync", m2.pending)
	}
	if m2.nextSeq != 0 {
		t.Errorf("nextSeq = %d, want 0 — the server's matching count restarts at zero", m2.nextSeq)
	}
}
