package server

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/syncevent"
)

// TestRejectionsRecordSyncEvents covers the wiring, which is the half a unit
// test of syncevent cannot reach: that the real rejection paths actually record
// something. A recorder nothing calls is worse than no recorder — it reports
// silence and the silence looks like health.
func TestRejectionsRecordSyncEvents(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	_, entry, cl := newOTTestService(t, "hello\n", 1, 2)
	entry.generation = 3

	// A generation the server does not have: rejected.
	if err := sendOpGen(t, cl, 1, 99, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	}); err == nil {
		t.Fatal("test setup: a mismatched generation should be rejected")
	}

	events, err := syncevent.Read(syncevent.ReadOptions{Kind: syncevent.GenerationMismatch})
	if err != nil {
		t.Fatalf("syncevent.Read: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("a rejected op recorded no sync event — the tool would report " +
			"nothing went wrong")
	}
	got := events[len(events)-1]
	if got.Component != "server" {
		t.Errorf("component = %q, want server", got.Component)
	}
	if got.BufID != 1 {
		t.Errorf("bufID = %d, want 1", got.BufID)
	}
	if got.Detail == "" {
		t.Error("detail is empty: the event says a mismatch happened but not which versions")
	}
}

// TestStaleBaseRecordsASyncEvent covers the other rejection path, which is the
// one that means silent corruption was prevented rather than a swap missed.
func TestStaleBaseRecordsASyncEvent(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	_, entry, cl := newOTTestService(t, "hello world\n", 1, 2)

	// Client 1 acknowledges through version 5, then sends an op based on 0 —
	// older than what it has already confirmed seeing, so the ops needed to
	// rebase it are gone.
	recordPruned(entry, 1, 5)
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	}); err == nil {
		t.Fatal("test setup: a base older than the acknowledged version should be refused")
	}

	events, err := syncevent.Read(syncevent.ReadOptions{Kind: syncevent.StaleBase})
	if err != nil {
		t.Fatalf("syncevent.Read: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("a stale-base rejection recorded no sync event")
	}
}
