package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// newOTTestService builds a service with one buffer held by the given clients.
func newOTTestService(t *testing.T, content string, clientIDs ...uint64) (*editorService, *bufferEntry, proto.EditorService) {
	t.Helper()
	dir := t.TempDir()
	clients := map[uint64]struct{}{}
	since := map[uint64]uint64{}
	for _, id := range clientIDs {
		clients[id] = struct{}{}
		since[id] = 0
	}
	entry := &bufferEntry{
		buf:           document.New(filepath.Join(dir, "a.go"), content),
		canonPath:     canonicalPath(filepath.Join(dir, "a.go")),
		clients:       clients,
		sinceByClient: since,
	}
	s := &editorService{
		cfg:         &config.Config{},
		buffers:     map[uint32]*bufferEntry{1: entry},
		clientMap:   map[uint64]*clientEntry{},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	t.Cleanup(cl.Release)
	return s, entry, cl
}

func sendOp(t *testing.T, cl proto.EditorService, clientID uint64, baseVersion uint64, op document.Op) error {
	t.Helper()
	return sendOpGen(t, cl, clientID, 0, baseVersion, op)
}

// sendOpGen is sendOp with an explicit generation, for tests that edit a buffer
// after a wholesale swap has bumped it.
func sendOpGen(t *testing.T, cl proto.EditorService, clientID, generation, baseVersion uint64, op document.Op) error {
	t.Helper()
	fut, rel := cl.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(clientID)
		p.SetBufferId(1)
		p.SetGeneration(generation)
		p.SetBaseVersion(baseVersion)
		po, err := p.NewOp()
		if err != nil {
			return err
		}
		switch op.Type {
		case document.OpInsert:
			po.SetType(proto.EditOp_OpType_insert)
			po.SetInsertLine(uint32(op.InsertLine))
			po.SetInsertCol(uint32(op.InsertCol))
			return po.SetInsertText(op.InsertText)
		case document.OpDelete:
			po.SetType(proto.EditOp_OpType_delete)
			po.SetFromLine(uint32(op.FromLine))
			po.SetFromCol(uint32(op.FromCol))
			po.SetToLine(uint32(op.ToLine))
			po.SetToCol(uint32(op.ToCol))
			if op.ExpectText != "" {
				return po.SetExpectText(op.ExpectText)
			}
		}
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

func queueFor(s *editorService, entry *bufferEntry, clientID uint64) []document.Op {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]document.Op(nil), entry.outgoing[clientID]...)
}

// TestApplyOpRebasesAgainstUnseenRemoteOp is the core of the OT work: an op whose
// coordinates were computed before a concurrent remote edit must be rebased past
// it, not applied at face value.
//
// Client 1 edits, then client 2 sends an op it computed at baseVersion 0 — before
// client 1's edit existed. Applied verbatim it would land at the wrong offset.
func TestApplyOpRebasesAgainstUnseenRemoteOp(t *testing.T) {
	_, entry, cl := newOTTestService(t, "hello\n", 1, 2)

	// Client 1 inserts at the very start: "XXhello\n".
	if err := sendOp(t, cl, 1, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XX"}); err != nil {
		t.Fatalf("client 1 ApplyOp: %v", err)
	}
	// Client 2 has not seen that. It wants to insert "!" after "hello", which in
	// *its* view of the document is column 5.
	if err := sendOp(t, cl, 2, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "!"}); err != nil {
		t.Fatalf("client 2 ApplyOp: %v", err)
	}

	got := entry.buf.Content()
	if got != "XXhello!\n" {
		t.Errorf("content = %q, want %q — client 2's op must be rebased past client 1's, not applied at face value",
			got, "XXhello!\n")
	}
}

// TestApplyOpDoesNotRebaseAgainstAcknowledgedOps covers the other error: an op
// whose baseVersion says the client had already integrated the remote edit must
// NOT be rebased again. Double-transforming is as wrong as not transforming.
func TestApplyOpDoesNotRebaseAgainstAcknowledgedOps(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello\n", 1, 2)

	if err := sendOp(t, cl, 1, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XX"}); err != nil {
		t.Fatalf("client 1 ApplyOp: %v", err)
	}
	v := entry.buf.Version()

	// Client 2 acknowledges version v, so it is looking at "XXhello\n" and means
	// column 7 literally.
	if err := sendOp(t, cl, 2, v, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 7, InsertText: "!"}); err != nil {
		t.Fatalf("client 2 ApplyOp: %v", err)
	}
	if got := entry.buf.Content(); got != "XXhello!\n" {
		t.Errorf("content = %q, want %q — an acknowledged op must not be rebased a second time", got, "XXhello!\n")
	}
	if q := queueFor(s, entry, 2); len(q) != 0 {
		t.Errorf("client 2's queue still holds %d op(s) after acknowledging them: %+v", len(q), q)
	}
}

// TestApplyOpQueuesForOtherClientsOnly checks the broadcast rule: a client
// already holds what it sent, so its own op must not come back to it.
func TestApplyOpQueuesForOtherClientsOnly(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello\n", 1, 2, 3)

	if err := sendOp(t, cl, 1, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	if q := queueFor(s, entry, 1); len(q) != 0 {
		t.Errorf("sender's own queue got %d op(s), want 0: %+v", len(q), q)
	}
	for _, id := range []uint64{2, 3} {
		if q := queueFor(s, entry, id); len(q) != 1 {
			t.Errorf("client %d queue got %d op(s), want 1", id, len(q))
		}
	}
}

// TestGetUpdatesServesAndRetiresTheQueue covers delivery: a poll returns the
// client's queue, and only a poll that acknowledges those versions clears them.
func TestGetUpdatesServesAndRetiresTheQueue(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello\n", 1, 2)
	if err := sendOp(t, cl, 1, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}

	poll := func(since uint64) (int, uint64) {
		t.Helper()
		fut, rel := cl.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
			p.SetClientId(2)
			p.SetBufferId(1)
			p.SetSinceVersion(since)
			return nil
		})
		defer rel()
		res, err := fut.Struct()
		if err != nil {
			t.Fatalf("GetUpdates: %v", err)
		}
		ops, _ := res.Ops()
		return ops.Len(), res.Version()
	}

	n, ver := poll(0)
	if n != 1 {
		t.Fatalf("first poll returned %d ops, want 1", n)
	}
	// Still queued: an undelivered response must not retire ops.
	if q := queueFor(s, entry, 2); len(q) != 1 {
		t.Errorf("queue cleared on delivery rather than on acknowledgement (%d op(s) left)", len(q))
	}
	if n, _ := poll(ver); n != 0 {
		t.Errorf("second poll returned %d ops, want 0 after acknowledging version %d", n, ver)
	}
	if q := queueFor(s, entry, 2); len(q) != 0 {
		t.Errorf("queue still holds %d op(s) after acknowledgement", len(q))
	}
}

// TestPluginApplyEditReachesClientQueues is a regression test for a delivery
// gap the switch to per-client queues opened up. Under the previous shared-
// history delivery, ops applied straight to the buffer by a plugin reached
// clients implicitly, because GetUpdates read that history. Queues are fed
// explicitly, so a site that applies to the buffer without queueing leaves the
// edit on the server and in nobody's window — silently, with no error.
func TestPluginApplyEditReachesClientQueues(t *testing.T) {
	s, entry, _ := newOTTestService(t, "hello\n", 7)

	if err := s.PluginApplyEdit(1, []plugin.TextEdit{{
		FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 0, NewText: "P",
	}}); err != nil {
		t.Fatalf("PluginApplyEdit: %v", err)
	}
	if got := entry.buf.Content(); got != "Phello\n" {
		t.Fatalf("content = %q, want %q", got, "Phello\n")
	}
	if q := queueFor(s, entry, 7); len(q) != 1 {
		t.Errorf("client queue got %d op(s), want 1 — a plugin edit that reaches no queue is invisible to every window", len(q))
	}
}

// TestBufferSwapClearsOutgoingQueues covers the wholesale-swap sites: queued ops
// describe the old buffer object and cannot be rebased onto the new one, so they
// must be discarded rather than replayed. Clients learn of the swap from the
// generation bump and resync.
func TestBufferSwapClearsOutgoingQueues(t *testing.T) {
	s, entry, cl := newOTTestService(t, "hello\n", 1, 2)
	s.recDir = t.TempDir()
	if err := sendOp(t, cl, 1, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if len(queueFor(s, entry, 2)) != 1 {
		t.Fatal("test setup: client 2 should have a queued op")
	}

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

	if q := queueFor(s, entry, 2); len(q) != 0 {
		t.Errorf("queue survived a buffer swap with %d op(s); those describe the replaced buffer: %+v", len(q), q)
	}
}
