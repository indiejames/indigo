package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func opIns(line, col int, text string) document.Op {
	return document.Op{Type: document.OpInsert, InsertLine: line, InsertCol: col, InsertText: text}
}

// TestRebasePastPendingSkipsOpsTheServerAppliedFirst is the crux of the client
// half of the transform. An incoming op's coordinates already account for every
// op the server applied *before* it, including this client's own — rebasing
// past those again would double-count them. Only what the server had not yet
// applied counts.
func TestRebasePastPendingSkipsOpsTheServerAppliedFirst(t *testing.T) {
	pending := []pendingOp{
		{seq: 1, version: 4, ops: []document.Op{opIns(0, 0, "AA")}}, // applied before the remote op
		{seq: 2, version: 0, ops: []document.Op{opIns(0, 9, "B")}},  // still in flight
	}
	remote := document.Op{Version: 5, Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "R"}

	_, rebased := rebasePastPending(pending, remote)
	if len(rebased) != 1 {
		t.Fatalf("got %d ops, want 1", len(rebased))
	}
	// Only seq 2 is concurrent, and it is at column 9 — after the remote op's
	// column 2 — so the remote op's position is unchanged. Had seq 1 been
	// counted too, it would have been pushed to column 4.
	if got := rebased[0].InsertCol; got != 2 {
		t.Errorf("rebased column = %d, want 2 — the already-applied pending op must not be transformed against again", got)
	}
}

// TestRebasePastPendingRebasesConcurrentOps is the other direction: an op still
// in flight is not yet known to the server, so an incoming op must be moved past
// it before being applied to this client's buffer, which already has it.
func TestRebasePastPendingRebasesConcurrentOps(t *testing.T) {
	pending := []pendingOp{
		{seq: 1, version: 0, ops: []document.Op{opIns(0, 0, "AA")}},
	}
	remote := document.Op{Version: 5, Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "R"}

	updated, rebased := rebasePastPending(pending, remote)
	if len(rebased) != 1 || rebased[0].InsertCol != 4 {
		t.Errorf("rebased = %+v, want a single insert at column 4 (shifted past the in-flight \"AA\")", rebased)
	}
	// And the pending op is rewritten to account for the remote op, so the next
	// incoming op is measured against the right thing.
	if updated[0].ops[0].InsertCol != 0 {
		t.Errorf("pending op moved to column %d; it is before the remote op and should not have", updated[0].ops[0].InsertCol)
	}
}

// TestPrunePendingKeepsUnacknowledgedOps covers the rule that matters for
// correctness: an op the server has not confirmed must be kept however old,
// because incoming ops still have to be rebased past it.
func TestPrunePendingKeepsUnacknowledgedOps(t *testing.T) {
	pending := []pendingOp{
		{seq: 1, version: 3},
		{seq: 2, version: 0},
		{seq: 3, version: 9},
	}
	got := prunePending(pending, 5)
	if len(got) != 2 || got[0].seq != 2 || got[1].seq != 3 {
		t.Errorf("prunePending kept %+v, want the unacknowledged op and the one above the watermark", got)
	}
}

// TestConvergenceTwoClientsThroughBothHalves is the end-to-end check that the
// two halves of the transform fit together — the server rebasing an incoming op
// past other clients' ops, and the client rebasing an incoming op past its own
// in-flight ops.
//
// It models the classic interleaving: both clients edit from the same starting
// version without having seen each other's edit, each sends, and each then
// receives the other's. They must end up holding identical content.
func TestConvergenceTwoClientsThroughBothHalves(t *testing.T) {
	const start = "hello\n"

	// Client A inserts at the start; client B appends after "hello". Both
	// computed against version 0.
	opA := opIns(0, 0, "A")
	opB := opIns(0, 5, "B")

	// The server receives A first (version 1), then B. B is rebased past A,
	// exactly as the server's rebaseIncoming does.
	serverAfterA := applyTo(start, opA)
	rebasedB := document.Transform(opB, opA, false)
	serverFinal := applyToAll(serverAfterA, rebasedB)

	// Client A's buffer: it applied opA locally, and now receives B as the
	// server rebased it (version 2). A has nothing else in flight, so no
	// further rebasing is needed.
	clientA := applyToAll(applyTo(start, opA), stamp(rebasedB, 2))

	// Client B's buffer: it applied opB locally and still has it in flight when
	// A's op arrives at version 1. The client half rebases A's op past B's.
	pendingB := []pendingOp{{seq: 1, version: 0, ops: []document.Op{opB}}}
	_, rebasedForB := rebasePastPending(pendingB, document.Op{
		Version: 1, Type: opA.Type, InsertLine: opA.InsertLine, InsertCol: opA.InsertCol, InsertText: opA.InsertText,
	})
	clientB := applyToAll(applyTo(start, opB), rebasedForB)

	if clientA != serverFinal {
		t.Errorf("client A = %q, server = %q", clientA, serverFinal)
	}
	if clientB != serverFinal {
		t.Errorf("client B = %q, server = %q", clientB, serverFinal)
	}
	if clientA != clientB {
		t.Fatalf("clients diverged: A = %q, B = %q", clientA, clientB)
	}
	if serverFinal != "Ahello B\n" && serverFinal != "AhelloB\n" {
		t.Logf("converged on %q", serverFinal)
	}
}

func applyTo(content string, op document.Op) string {
	b := document.New("/tmp/t", content)
	b.Apply(op)
	return b.Content()
}

func applyToAll(content string, ops []document.Op) string {
	b := document.New("/tmp/t", content)
	for _, op := range ops {
		b.Apply(op)
	}
	return b.Content()
}

func stamp(ops []document.Op, version uint64) []document.Op {
	out := make([]document.Op, len(ops))
	for i, op := range ops {
		op.Version = version
		out[i] = op
	}
	return out
}

// TestUpdatesMsgRebasesRemoteOpPastPending covers the wiring, not just the
// helper. The previous tests call rebasePastPending directly, so they pass even
// if the updatesMsg handler never calls it — which is exactly the regression
// worth guarding, since the symptom is silent divergence rather than an error.
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
	m.pending = []pendingOp{{seq: 1, version: 0, ops: []document.Op{local}}}

	// A remote op arrives that the server computed against "hello\n": insert
	// "R" at column 2, i.e. after "he".
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "R"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	// Applied verbatim it would land at column 2 of "AAhello\n" — inside "AA" —
	// giving "AARhello\n". Rebased past the in-flight insert it lands after
	// "he" as intended.
	if got := m2.buf.Content(); got != "AAheRllo\n" {
		t.Errorf("buf.Content() = %q, want %q — the remote op must be rebased past this client's in-flight edit",
			got, "AAheRllo\n")
	}
}

// TestUpdatesMsgPrunesAcknowledgedPending checks the watermark side of the
// bookkeeping: once the client has caught up past an acknowledged op, keeping it
// would have later incoming ops rebased past an edit the server already
// accounts for.
func TestUpdatesMsgPrunesAcknowledgedPending(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.pending = []pendingOp{
		{seq: 1, version: 2, ops: []document.Op{opIns(0, 0, "A")}},
		{seq: 2, version: 0, ops: []document.Op{opIns(0, 1, "B")}},
	}

	updated, _ := m.Update(updatesMsg{bufID: m.bufID, version: 4, generation: 1})
	m2 := updated.(Model)

	if len(m2.pending) != 1 || m2.pending[0].seq != 2 {
		t.Errorf("pending = %+v, want only the unacknowledged op to survive a watermark of 4", m2.pending)
	}
}

// TestOpsAckedMsgStampsVersions covers acknowledgement routing: without the
// assigned version, an entry can never be pruned and can never be correctly
// partitioned against an incoming op.
func TestOpsAckedMsgStampsVersions(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.pending = []pendingOp{
		{seq: 1, version: 0, ops: []document.Op{opIns(0, 0, "A")}},
		{seq: 2, version: 0, ops: []document.Op{opIns(0, 1, "B")}},
	}

	updated, _ := m.Update(opsAckedMsg{bufID: m.bufID, acks: []opAck{{seq: 2, version: 7}}})
	m2 := updated.(Model)

	if len(m2.pending) != 2 {
		t.Fatalf("pending length changed: %+v", m2.pending)
	}
	if m2.pending[0].version != 0 {
		t.Errorf("seq 1 version = %d, want 0 (not acknowledged)", m2.pending[0].version)
	}
	if m2.pending[1].version != 7 {
		t.Errorf("seq 2 version = %d, want 7", m2.pending[1].version)
	}
}

// TestOpsAckedMsgIsRoutable: acknowledgements must reach their own buffer even
// when the user has switched tabs, or that buffer's pending queue never drains
// and every later op is rebased against ops the server already has.
func TestOpsAckedMsgIsRoutable(t *testing.T) {
	var msg any = opsAckedMsg{bufID: 9}
	rm, ok := msg.(RoutableMsg)
	if !ok {
		t.Fatal("opsAckedMsg must implement RoutableMsg")
	}
	if rm.RouteBufID() != 9 {
		t.Errorf("RouteBufID() = %d, want 9", rm.RouteBufID())
	}
}
