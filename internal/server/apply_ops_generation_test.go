package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// newApplyOpsTestService returns a service holding one buffer whose generation
// is already `gen`, plus a capability to call it through.
func newApplyOpsTestService(t *testing.T, content string, gen uint64) (*editorService, proto.EditorService) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:           document.New(path, content),
				canonPath:     canonicalPath(path),
				generation:    gen,
				clients:       map[uint64]struct{}{1: {}},
				sinceByClient: map[uint64]uint64{},
			},
		},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	t.Cleanup(cl.Release)
	return s, cl
}

// callApplyOps sends a single-op batch claiming the given generation.
func callApplyOps(t *testing.T, cl proto.EditorService, generation uint64) error {
	t.Helper()
	fut, rel := cl.ApplyOps(context.Background(), func(p proto.EditorService_applyOps_Params) error {
		p.SetBufferId(1)
		p.SetClientId(1)
		p.SetGeneration(generation)
		list, err := p.NewOps(1)
		if err != nil {
			return err
		}
		op := list.At(0)
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(0)
		return op.SetInsertText("XX")
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// TestApplyOpsRejectsStaleGeneration is a regression test: applyOp gained a
// generation parameter precisely so a client unaware of a wholesale buffer swap
// can't have its now-meaningless coordinates applied to the new buffer, but
// applyOps never got one — despite being the call whose coordinates are most
// likely to be stale. Its callers compute them from content read in an earlier,
// separate round trip (a workspace search-and-replace hit, or an agent's
// read_file), so there is real time for a swap to land in between.
func TestApplyOpsRejectsStaleGeneration(t *testing.T) {
	s, cl := newApplyOpsTestService(t, "hello\n", 3)

	err := callApplyOps(t, cl, 2) // client still believes it is generation 2
	if err == nil {
		t.Fatal("ApplyOps with a stale generation was accepted; it must be rejected so the caller re-reads")
	}
	if !strings.Contains(err.Error(), "generation mismatch") {
		t.Errorf("error = %q, want it to mention a generation mismatch", err)
	}

	s.mu.Lock()
	got := s.buffers[1].buf.Content()
	s.mu.Unlock()
	if got != "hello\n" {
		t.Errorf("buffer content = %q, want it untouched — a rejected batch must not be partially applied", got)
	}
}

// TestApplyOpsAcceptsMatchingGeneration is the positive control: the guard must
// not break the ordinary path.
func TestApplyOpsAcceptsMatchingGeneration(t *testing.T) {
	s, cl := newApplyOpsTestService(t, "hello\n", 3)

	if err := callApplyOps(t, cl, 3); err != nil {
		t.Fatalf("ApplyOps with a matching generation errored: %v", err)
	}

	s.mu.Lock()
	got := s.buffers[1].buf.Content()
	s.mu.Unlock()
	if got != "XXhello\n" {
		t.Errorf("buffer content = %q, want %q", got, "XXhello\n")
	}
}

// TestDiscardRecoveryReturnsBumpedGeneration is a regression test for the other
// half of this pair: DiscardRecovery replaces the buffer object wholesale and
// bumps entry.generation, but its capnp result carried only content — so no
// caller could adopt the new value, and every client's next GetUpdates poll saw
// a mismatch and resynced for nothing (a resync that also marks the buffer
// dirty, which is backwards here: discarding recovery is what makes the buffer
// match disk).
func TestDiscardRecoveryReturnsBumpedGeneration(t *testing.T) {
	s, cl := newApplyOpsTestService(t, "recovered\n", 4)
	s.recDir = t.TempDir()

	fut, rel := cl.DiscardRecovery(context.Background(), func(p proto.EditorService_discardRecovery_Params) error {
		p.SetBufferId(1)
		p.SetClientId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("DiscardRecovery errored: %v", err)
	}

	s.mu.Lock()
	serverGen := s.buffers[1].generation
	s.mu.Unlock()

	if serverGen != 5 {
		t.Fatalf("entry.generation = %d, want 5 after the swap", serverGen)
	}
	if got := res.Generation(); got != serverGen {
		t.Errorf("result generation = %d, want %d — the caller cannot adopt what isn't returned", got, serverGen)
	}
}
