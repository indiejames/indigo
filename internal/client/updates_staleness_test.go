package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestUpdatesMsgDiscardedForOtherBuffer is a regression test: updatesMsg
// carried no bufID at all, and isn't routed by App to a specific buffer
// without one — so a GetUpdates issued by one tab and answered after the
// user switched tabs was dispatched to whatever buffer was active by then,
// applying another file's ops to it.
func TestUpdatesMsgDiscardedForOtherBuffer(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.bufID = 7
	m.version = 3
	m.generation = 1
	m.generationKnown = true

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XXX", Version: 4}
	// bufID 9: a poll result belonging to a different buffer entirely.
	updated, _ := m.Update(updatesMsg{bufID: 9, ops: []document.Op{op}, version: 4, generation: 1})
	m2 := updated.(Model)

	if m2.buf.Content() != "hello\n" {
		t.Errorf("buf.Content() = %q, want unchanged — ops for another buffer must not be applied", m2.buf.Content())
	}
	if m2.version != 3 {
		t.Errorf("version = %d, want 3 — another buffer's version must not become our polling watermark", m2.version)
	}
}

// TestUpdatesMsgIsRoutable checks updatesMsg reaches App's bufID-routing
// path rather than the generic active-buffer-only fallback.
func TestUpdatesMsgIsRoutable(t *testing.T) {
	var msg any = updatesMsg{bufID: 42}
	rm, ok := msg.(RoutableMsg)
	if !ok {
		t.Fatal("updatesMsg must implement RoutableMsg so a straggler reaches the buffer it was about")
	}
	if got := rm.RouteBufID(); got != 42 {
		t.Errorf("RouteBufID() = %d, want 42", got)
	}
}

// TestUpdatesMsgSkipsAlreadyAppliedOps is a regression test for duplicate
// application. Polls go out every 120ms with a 2s timeout and no in-flight
// guard, and m.version only advances when a response is *handled*, so two
// outstanding polls carry the same sinceVersion and the server answers both
// with the same ops. Applying the second response's ops again duplicated
// the edit.
func TestUpdatesMsgSkipsAlreadyAppliedOps(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.version = 0
	m.generation = 1
	m.generationKnown = true

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: " world", Version: 1}
	msg := updatesMsg{ops: []document.Op{op}, version: 1, generation: 1}

	updated, _ := m.Update(msg)
	m2 := updated.(Model)
	if m2.buf.Content() != "hello world\n" {
		t.Fatalf("after first response: buf.Content() = %q, want %q", m2.buf.Content(), "hello world\n")
	}
	if m2.version != 1 {
		t.Fatalf("after first response: version = %d, want 1", m2.version)
	}
	undoDepth := len(m2.undoStack)

	// The second in-flight poll's response: same sinceVersion, same ops.
	updated2, _ := m2.Update(msg)
	m3 := updated2.(Model)
	if m3.buf.Content() != "hello world\n" {
		t.Errorf("after duplicate response: buf.Content() = %q, want %q — the op must not be applied twice",
			m3.buf.Content(), "hello world\n")
	}
	if len(m3.undoStack) != undoDepth {
		t.Errorf("undoStack grew from %d to %d on a duplicate response — no edit happened, so no undo entry belongs",
			undoDepth, len(m3.undoStack))
	}
}

// TestUpdatesMsgDoesNotRewindVersion covers the other half of an
// out-of-order response: a late one reports the version the buffer had when
// it was built, which can be behind where a response handled since has
// already moved us. Taking it verbatim would rewind the polling watermark
// and re-request ops already applied.
func TestUpdatesMsgDoesNotRewindVersion(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	m.version = 9
	m.generation = 1
	m.generationKnown = true

	updated, _ := m.Update(updatesMsg{version: 4, generation: 1})
	m2 := updated.(Model)

	if m2.version != 9 {
		t.Errorf("version = %d, want 9 — a late response must not rewind the watermark", m2.version)
	}
}

// TestUpdatesMsgIgnoresOlderGeneration covers a response built before a
// buffer swap that we have already learned about and resynced past.
// generation only increases, so this is unambiguously a straggler; treating
// it as a mismatch would send us round the resync loop a second time for a
// swap already handled.
func TestUpdatesMsgIgnoresOlderGeneration(t *testing.T) {
	m := newTestModel("resynced content\n")
	m.rpc = &RPC{}
	m.version = 2
	m.generation = 5
	m.generationKnown = true

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "stale", Version: 3}
	updated, cmd := m.Update(updatesMsg{ops: []document.Op{op}, version: 3, generation: 4})
	m2 := updated.(Model)

	if m2.buf.Content() != "resynced content\n" {
		t.Errorf("buf.Content() = %q, want unchanged — ops from a superseded generation must not be applied",
			m2.buf.Content())
	}
	if cmd != nil {
		t.Error("expected no resync command: the swap this response predates was already handled")
	}
	if m2.generation != 5 {
		t.Errorf("generation = %d, want 5 — generation must not move backwards", m2.generation)
	}
}
