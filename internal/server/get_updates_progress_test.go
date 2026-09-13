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

// TestGetUpdatesRecordsProgressOnBothReturnPaths guards the refactor that moved
// recordClientProgress to after the response is encoded (so a failure while
// encoding, or a response that never reaches the client, can't retire ops via
// TrimHistory that the client never received). GetUpdates has two success
// returns — the early one when nothing is left after filtering out the
// caller's own ops, and the one after encoding the ops list — and the
// watermark has to advance on both. Dropping it from either is the realistic
// way to get this wrong, and would silently stop TrimHistory from ever
// reclaiming history for a client that mostly polls idle.
func TestGetUpdatesRecordsProgressOnBothReturnPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")

	buf := document.New(path, "package main\n")
	entry := &bufferEntry{
		buf:           buf,
		canonPath:     canonicalPath(path),
		clients:       map[uint64]struct{}{1: {}},
		sinceByClient: map[uint64]uint64{},
	}
	s := &editorService{
		cfg:         &config.Config{},
		buffers:     map[uint32]*bufferEntry{1: entry},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	poll := func(since uint64) uint64 {
		t.Helper()
		fut, rel := cl.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
			p.SetBufferId(1)
			p.SetClientId(1)
			p.SetSinceVersion(since)
			return nil
		})
		defer rel()
		res, err := fut.Struct()
		if err != nil {
			t.Fatalf("GetUpdates(since=%d): %v", since, err)
		}
		return res.Version()
	}

	// Path 1: nothing to send. Version is 0, so the ops list is empty and
	// GetUpdates takes the early return.
	if got := poll(0); got != 0 {
		t.Fatalf("test setup: version = %d, want 0", got)
	}
	s.mu.Lock()
	got := entry.sinceByClient[1]
	s.mu.Unlock()
	if got != 0 {
		t.Errorf("after empty poll: sinceByClient[1] = %d, want 0", got)
	}

	// Path 2: a foreign op (client 2) survives the filter, so the response
	// carries ops and GetUpdates takes the encoded return.
	buf.Apply(document.Op{ClientID: 2, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
	ver := poll(0)
	if ver != 1 {
		t.Fatalf("after a foreign op: reported version = %d, want 1", ver)
	}
	s.mu.Lock()
	got = entry.sinceByClient[1]
	s.mu.Unlock()
	if got != ver {
		t.Errorf("after a poll that returned ops: sinceByClient[1] = %d, want %d — "+
			"the watermark must advance on the encoded return path too", got, ver)
	}
}
