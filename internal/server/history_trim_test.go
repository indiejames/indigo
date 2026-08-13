package server

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

func applyInsertOp(t *testing.T, client proto.EditorService, clientID uint64, bufID uint32, text string) {
	t.Helper()
	fut, rel := client.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(clientID)
		p.SetBufferId(bufID)
		op, err := p.NewOp()
		if err != nil {
			return err
		}
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(0)
		return op.SetInsertText(text)
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("ApplyOp errored: %v", err)
	}
}

// TestHistoryTrimsForSoloClient is a regression test for the unbounded
// document.Buffer.history growth backlog item: a single long-running client
// that keeps a buffer open and keeps editing must not accumulate an
// ever-growing op history, since it is always caught up on its own edits.
func TestHistoryTrimsForSoloClient(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:           document.New("/tmp/x.go", ""),
				clients:       map[uint64]struct{}{1: {}},
				sinceByClient: map[uint64]uint64{1: 0},
			},
		},
		lspMgr:    lsp.NewManager("/tmp", nil),
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	const numOps = historyTrimThreshold + 50
	for i := 0; i < numOps; i++ {
		applyInsertOp(t, client, 1, 1, "x")
	}

	if got := s.buffers[1].buf.HistoryLen(); got >= numOps {
		t.Fatalf("HistoryLen = %d after %d ops from a single caught-up client; want it trimmed well below %d", got, numOps, numOps)
	}
}

// TestHistoryTrimBlockedByLaggingClient verifies that history is NOT
// reclaimed out from under a second connected client that hasn't caught up
// yet (via GetUpdates or its own ApplyOp) — trimming past what a client
// might still request would silently corrupt that client's next sync.
func TestHistoryTrimBlockedByLaggingClient(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:           document.New("/tmp/x.go", ""),
				clients:       map[uint64]struct{}{1: {}, 2: {}},
				sinceByClient: map[uint64]uint64{1: 0, 2: 0},
			},
		},
		lspMgr:    lsp.NewManager("/tmp", nil),
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	const numOps = historyTrimThreshold + 50
	for i := 0; i < numOps; i++ {
		applyInsertOp(t, client, 1, 1, "x")
	}

	// Client 2 never polled, so its watermark is still 0 — nothing may be trimmed.
	if got := s.buffers[1].buf.HistoryLen(); got != numOps {
		t.Fatalf("HistoryLen = %d, want %d (untrimmed) while client 2 hasn't caught up", got, numOps)
	}

	// Once client 2 catches up via GetUpdates, the now-fully-acknowledged
	// history should be reclaimed.
	fut, rel := client.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(2)
		p.SetBufferId(1)
		p.SetSinceVersion(0)
		return nil
	})
	if _, err := fut.Struct(); err != nil {
		rel()
		t.Fatalf("GetUpdates errored: %v", err)
	}
	rel()

	if got := s.buffers[1].buf.HistoryLen(); got != 0 {
		t.Fatalf("HistoryLen = %d after client 2 caught up, want 0", got)
	}
}

// TestGetUpdatesVersionMatchesReturnedOps is GetUpdates' integration-level
// counterpart to TestOpsSinceAndVersionAtomicUnderConcurrentApply
// (internal/document): it drives a concurrent direct buf.Apply — the same
// pattern plugin_bridge.go and server_move_to_file.go use, which bypasses
// the capnp call queue entirely and so can genuinely interleave with a
// GetUpdates RPC in flight — against repeated GetUpdates polls, and asserts
// the response's reported version always matches the version of the last op
// it actually returned. Before GetUpdates switched to the atomic
// OpsSinceAndVersion, a concurrent Apply landing between the old separate
// OpsSince/Version reads could report a version the returned ops didn't
// cover, permanently desyncing the polling client.
func TestGetUpdatesVersionMatchesReturnedOps(t *testing.T) {
	entry := &bufferEntry{
		buf:           document.New("/tmp/x.go", ""),
		clients:       map[uint64]struct{}{1: {}, 2: {}},
		sinceByClient: map[uint64]uint64{1: 0, 2: 0},
	}
	s := &editorService{
		buffers: map[uint32]*bufferEntry{1: entry},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	const n = 1500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			entry.buf.Apply(document.Op{
				ClientID:   1,
				Type:       document.OpInsert,
				InsertLine: 0,
				InsertCol:  0,
				InsertText: "x",
			})
		}
	}()

	for i := 0; i < n; i++ {
		fut, rel := client.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
			p.SetClientId(2)
			p.SetBufferId(1)
			p.SetSinceVersion(0)
			return nil
		})
		res, err := fut.Struct()
		if err != nil {
			rel()
			t.Fatalf("GetUpdates errored: %v", err)
		}
		ops, opsErr := res.Ops()
		ver := res.Version()
		if opsErr != nil {
			rel()
			t.Fatalf("res.Ops(): %v", opsErr)
		}
		var lastOpVersion uint64
		hasOps := ops.Len() > 0
		if hasOps {
			lastOpVersion = ops.At(ops.Len() - 1).Version()
		}
		rel()
		if !hasOps {
			continue // Apply goroutine hasn't landed its first op yet
		}
		if lastOpVersion != ver {
			t.Fatalf("GetUpdates: last returned op version=%d, reported version=%d — must always match under a concurrent Apply", lastOpVersion, ver)
		}
	}
	<-done
}
