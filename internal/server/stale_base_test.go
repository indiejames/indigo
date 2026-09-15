package server

import (
	"context"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

func pollAs(t *testing.T, cl proto.EditorService, clientID uint64, since uint64) uint64 {
	t.Helper()
	fut, rel := cl.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(clientID)
		p.SetBufferId(1)
		p.SetSinceVersion(since)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	return res.Version()
}

// TestApplyOpRejectsBaseVersionOlderThanTheQueueCanRebase closes the last path
// that could corrupt a buffer silently rather than surfacing as a resync.
//
// The server rebases an incoming op past the ops in that client's outgoing queue
// with a version above the op's baseVersion. But the queue is pruned at whatever
// sinceVersion the client's polls report, so an op whose baseVersion predates
// that prune has nothing left to be rebased against — and was applied anyway, at
// coordinates describing content the server no longer has.
//
// The client can no longer produce this (it only polls when its send queue is
// idle), but nothing on the server enforced it: a plugin, an agent tool, or a
// future client change could still send a stale baseVersion and silently corrupt
// the buffer. Rejecting turns that into the resync the fallback exists for.
func TestApplyOpRejectsBaseVersionOlderThanTheQueueCanRebase(t *testing.T) {
	_, entry, cl := newOTTestService(t, "ab\n", 1, 2)

	// Client 1 edits; the op lands in client 2's outgoing queue.
	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X",
	}); err != nil {
		t.Fatalf("client 1 ApplyOp: %v", err)
	}
	ver := pollAs(t, cl, 2, 0) // delivers it
	pollAs(t, cl, 2, ver)      // acknowledges it, pruning the queue

	before := entry.buf.Content()

	// Client 2 now sends an op computed before it saw client 1's edit. The ops
	// needed to rebase it are gone.
	err := sendOp(t, cl, 2, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 2, InsertText: "Y",
	})
	if err == nil {
		t.Fatalf("a stale baseVersion was accepted; the op was applied at coordinates "+
			"the server can no longer rebase, giving %q", entry.buf.Content())
	}
	if !strings.Contains(err.Error(), "base version") {
		t.Errorf("error = %q, want it to name the stale base version", err)
	}
	if got := entry.buf.Content(); got != before {
		t.Errorf("buffer changed to %q despite the rejection; a rejected op must not be applied", got)
	}
}

// TestApplyOpAcceptsBaseVersionAtThePruneWatermark is the boundary: an op based
// exactly on what the client last acknowledged is fine, because everything above
// that is still queued. Rejecting it would make ordinary editing resync, which
// is precisely what this step exists to avoid.
func TestApplyOpAcceptsBaseVersionAtThePruneWatermark(t *testing.T) {
	_, entry, cl := newOTTestService(t, "ab\n", 1, 2)

	if err := sendOp(t, cl, 1, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X",
	}); err != nil {
		t.Fatalf("client 1 ApplyOp: %v", err)
	}
	ver := pollAs(t, cl, 2, 0)
	pollAs(t, cl, 2, ver)

	if err := sendOp(t, cl, 2, ver, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "Y",
	}); err != nil {
		t.Fatalf("an op based exactly on the acknowledged version was rejected: %v", err)
	}
	if got := entry.buf.Content(); got != "XabY\n" {
		t.Errorf("content = %q, want %q", got, "XabY\n")
	}
}

// TestBufferSwapResetsPerClientWatermarks is a regression test for a rejection
// loop the stale-base guard can otherwise cause.
//
// A wholesale swap replaces the buffer with a fresh one at version 0, and every
// client resyncs to that. But the per-client watermarks are counted in the *old*
// buffer's version space, so a client's next op — legitimately based on version
// 0 — looks older than an acknowledged version in the dozens, and is refused.
// The client resyncs, tries again, is refused again: the buffer becomes
// permanently unwritable until something drops the entry.
func TestBufferSwapResetsPerClientWatermarks(t *testing.T) {
	s, entry, cl := newOTTestService(t, "ab\n", 1, 2)
	s.recDir = t.TempDir()

	// Build up some history and let client 2 acknowledge it, setting its
	// watermark well above zero.
	for i := 0; i < 3; i++ {
		if err := sendOp(t, cl, 1, uint64(i), document.Op{
			Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X",
		}); err != nil {
			t.Fatalf("seeding ApplyOp: %v", err)
		}
	}
	ver := pollAs(t, cl, 2, 0)
	pollAs(t, cl, 2, ver)

	s.mu.Lock()
	watermark := entry.prunedThrough[2]
	s.mu.Unlock()
	if watermark == 0 {
		t.Fatal("test setup: client 2's watermark should be above zero")
	}

	// A wholesale swap: the buffer is replaced and restarts at version 0.
	fut, rel := cl.DiscardRecovery(context.Background(), func(p proto.EditorService_discardRecovery_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		return nil
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatalf("DiscardRecovery: %v", err)
	}

	// Client 2 resyncs to the new buffer — adopting its generation and version
	// 0 — and edits it.
	s.mu.Lock()
	gen := entry.generation
	s.mu.Unlock()
	if err := sendOpGen(t, cl, 2, gen, 0, document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "Y",
	}); err != nil {
		t.Fatalf("an op on the freshly swapped buffer was rejected: %v — the watermarks are counted "+
			"in the replaced buffer's version space and must be cleared with it", err)
	}
}
