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
