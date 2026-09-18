//go:build indigo_debug

package server

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/faultinject"
	"github.com/indiejames/indigo/internal/proto"
	"github.com/indiejames/indigo/internal/syncevent"
)

// These tests exist to show the injection is worth having: each drives a path
// that otherwise needs a race to land a particular way, which is precisely why
// this class of bug kept shipping.

// TestInjectedGenerationBumpIsIndistinguishableFromARealSwap checks the
// injected fault reaches the *same* rejection a format-on-save would, rather
// than a special case that only looks like one.
func TestInjectedGenerationBumpIsIndistinguishableFromARealSwap(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Cleanup(faultinject.Reset)
	_, entry, cl := newOTTestService(t, "hello\n", 1, 2)

	faultinject.BumpGenerationOnNextApplies(1)
	// The client's generation is correct as it sends; the bump lands first.
	err := sendOpGen(t, cl, 1, entry.generation, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	})
	if err == nil {
		t.Fatal("an injected generation bump did not cause a rejection")
	}

	events, rerr := syncevent.Read(syncevent.ReadOptions{Kind: syncevent.GenerationMismatch})
	if rerr != nil {
		t.Fatalf("syncevent.Read: %v", rerr)
	}
	if len(events) == 0 {
		t.Error("the injected swap produced no generation_mismatch event, so it is " +
			"not reaching the path a real swap reaches")
	}
}

// TestInjectedDroppedPollIsRecoverable is the one worth having. A dropped poll
// must lose nothing: the ops have to arrive on the next poll instead. Getting
// this wrong — reporting the new version with no ops — loses them permanently
// and silently, which is the failure Phase 8 item 36 was about.
func TestInjectedDroppedPollIsRecoverable(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Cleanup(faultinject.Reset)
	s, entry, cl := newOTTestService(t, "hello\n", 1, 2)

	// An op from client 2, queued for client 1.
	s.mu.Lock()
	applyServerOriginated(entry, 2, document.Op{
		ClientID: 2, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	})
	s.mu.Unlock()

	poll := func() (ops int, version uint64) {
		t.Helper()
		fut, rel := cl.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
			p.SetBufferId(1)
			p.SetClientId(1)
			p.SetSinceVersion(0)
			return nil
		})
		defer rel()
		res, err := fut.Struct()
		if err != nil {
			t.Fatalf("GetUpdates: %v", err)
		}
		list, err := res.Ops()
		if err != nil {
			t.Fatalf("Ops: %v", err)
		}
		return list.Len(), res.Version()
	}

	faultinject.DropNextPollResponses(1)
	gotOps, gotVer := poll()
	if gotOps != 0 {
		t.Fatalf("dropped poll returned %d ops, want 0", gotOps)
	}
	if gotVer != 0 {
		t.Errorf("dropped poll reported version %d, want 0 — reporting the new "+
			"version with no ops makes the client believe it has seen them, and "+
			"the watermark only moves forward, so they are lost for good", gotVer)
	}

	// The retry must deliver what the drop withheld.
	gotOps, gotVer = poll()
	if gotOps != 1 {
		t.Errorf("the poll after a dropped one returned %d ops, want 1 — a dropped "+
			"response must be recoverable", gotOps)
	}
	if gotVer != 1 {
		t.Errorf("recovered poll reported version %d, want 1", gotVer)
	}
}

// TestInjectedApplyFailureIsRefusedAndRecorded covers the simplest fault, which
// is also the one a client-side resync test needs in order to be driven at all.
func TestInjectedApplyFailureIsRefusedAndRecorded(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Cleanup(faultinject.Reset)
	_, entry, cl := newOTTestService(t, "hello\n", 1, 2)
	before := entry.buf.Content()

	faultinject.FailNextApplyOps(2)
	for i := 0; i < 2; i++ {
		if err := sendOp(t, cl, 1, 0, document.Op{
			Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
		}); err == nil {
			t.Fatalf("attempt %d: an injected failure was accepted", i+1)
		}
	}
	if got := entry.buf.Content(); got != before {
		t.Errorf("buffer = %q, want %q — a refused op must not reach the buffer", got, before)
	}

	// And the third succeeds: the counter is consumed, not latched.
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x",
	}); err != nil {
		t.Errorf("the op after the armed failures was refused too: %v", err)
	}
}
