package server

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// reportingCallback is a partial ClientCallback implementing only
// ReportBufferState.
//
// block, when non-nil, is waited on before answering, simulating a window that
// is too busy to reply. By default it also gives up when the call's context
// expires, which is what the real client-side handler does — and what an
// in-process fake must do to be usable at all, since cancelling the caller's
// context does not resolve a future whose callee never returns. Set ignoreCtx
// to get the harsher case (a client whose process is stopped, so nothing ever
// runs) and confirm the collector's own deadline still bounds the wait.
type reportingCallback struct {
	proto.ClientCallback_Server
	sum       []byte
	known     bool
	ver       uint64
	gen       uint64
	block     chan struct{}
	ignoreCtx bool
}

func (f *reportingCallback) ReportBufferState(ctx context.Context, call proto.ClientCallback_reportBufferState) error {
	if f.block != nil {
		if f.ignoreCtx {
			<-f.block
		} else {
			select {
			case <-f.block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetKnown(f.known)
	res.SetVersion(f.ver)
	res.SetGeneration(f.gen)
	return res.SetContentSha256(f.sum)
}

// TestCheckBufferConsistencyCollectsClientAnswers covers the three answers the
// check has to tell apart: a client whose content matches, one whose content
// differs, and one that never answers at all. Conflating the last with "does
// not hold that buffer" would send a diagnosis in the wrong direction.
func TestCheckBufferConsistencyCollectsClientAnswers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	content := "package main\n"
	serverSum := sha256.Sum256([]byte(content))
	otherSum := sha256.Sum256([]byte("something else\n"))

	matching := &reportingCallback{sum: serverSum[:], known: true, ver: 4, gen: 2}
	differing := &reportingCallback{sum: otherSum[:], known: true, ver: 4, gen: 2}
	wedged := &reportingCallback{sum: serverSum[:], known: true, block: make(chan struct{})}
	defer close(wedged.block)

	cbMatching := proto.ClientCallback_ServerToClient(matching)
	defer cbMatching.Release()
	cbDiffering := proto.ClientCallback_ServerToClient(differing)
	defer cbDiffering.Release()
	cbWedged := proto.ClientCallback_ServerToClient(wedged)
	defer cbWedged.Release()

	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:        document.New(path, content),
				canonPath:  canonicalPath(path),
				generation: 2,
				clients:    map[uint64]struct{}{1: {}, 2: {}, 3: {}},
			},
		},
		clientMap: map[uint64]*clientEntry{
			1: {callback: cbMatching, connID: 11},
			2: {callback: cbDiffering, connID: 22},
			3: {callback: cbWedged, connID: 33},
		},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	fut, rel := cl.CheckBufferConsistency(context.Background(), func(p proto.EditorService_checkBufferConsistency_Params) error {
		p.SetBufferId(0)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("CheckBufferConsistency: %v", err)
	}
	list, err := res.Buffers()
	if err != nil {
		t.Fatalf("Buffers: %v", err)
	}
	if list.Len() != 1 {
		t.Fatalf("got %d buffers, want 1", list.Len())
	}
	buf := list.At(0)
	gotServerSum, _ := buf.ServerSha256()
	if string(gotServerSum) != string(serverSum[:]) {
		t.Error("server sha256 mismatch")
	}

	clients, err := buf.Clients()
	if err != nil {
		t.Fatalf("Clients: %v", err)
	}
	if clients.Len() != 3 {
		t.Fatalf("got %d client reports, want 3", clients.Len())
	}
	// Sorted by client id, so a report diffs cleanly against a previous one.
	byID := map[uint64]proto.ClientBufferReport{}
	for i := 0; i < clients.Len(); i++ {
		byID[clients.At(i).ClientId()] = clients.At(i)
	}

	if c := byID[1]; !c.Answered() || !c.Known() {
		t.Errorf("client 1: answered=%v known=%v, want both true", c.Answered(), c.Known())
	} else if sum, _ := c.ContentSha256(); string(sum) != string(serverSum[:]) {
		t.Error("client 1 should have reported the server's hash")
	}

	if c := byID[2]; !c.Answered() {
		t.Error("client 2 should have answered")
	} else if sum, _ := c.ContentSha256(); string(sum) == string(serverSum[:]) {
		t.Error("client 2 should have reported a different hash")
	}

	// The wedged client must come back as "did not answer" rather than as an
	// absent or zero-valued report.
	if c := byID[3]; c.Answered() {
		t.Error("client 3 is wedged and must be reported as not having answered")
	}
}

// TestCheckBufferConsistencyOneWedgedClientDoesNotStallOthers verifies the
// per-client fan-out: serially with a shared budget, one wedged window would
// consume the whole timeout and make every other client look unresponsive.
func TestCheckBufferConsistencyOneWedgedClientDoesNotStallOthers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	sum := sha256.Sum256([]byte("x\n"))

	wedged := &reportingCallback{known: true, sum: sum[:], block: make(chan struct{})}
	defer close(wedged.block)
	ok1 := &reportingCallback{known: true, sum: sum[:]}
	ok2 := &reportingCallback{known: true, sum: sum[:]}

	cbW := proto.ClientCallback_ServerToClient(wedged)
	defer cbW.Release()
	cb1 := proto.ClientCallback_ServerToClient(ok1)
	defer cb1.Release()
	cb2 := proto.ClientCallback_ServerToClient(ok2)
	defer cb2.Release()

	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "x\n"), clients: map[uint64]struct{}{1: {}, 2: {}, 3: {}}},
		},
		clientMap: map[uint64]*clientEntry{
			1: {callback: cbW},
			2: {callback: cb1},
			3: {callback: cb2},
		},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	start := time.Now()
	fut, rel := cl.CheckBufferConsistency(context.Background(), func(p proto.EditorService_checkBufferConsistency_Params) error {
		p.SetBufferId(1)
		return nil
	})
	// defer, not an immediate rel(): releasing before reading the result
	// invalidates the underlying capnp message and panics with "slice bounds
	// out of range ... capacity 0". Documented in CLAUDE.md's Phase 4 notes,
	// and duly walked into again here.
	defer rel()
	res, err := fut.Struct()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("CheckBufferConsistency: %v", err)
	}

	// One wedged client costs one timeout, not three.
	if elapsed > 3*consistencyCallTimeout {
		t.Errorf("took %v with one wedged client; per-client timeouts should bound this near %v",
			elapsed, consistencyCallTimeout)
	}

	list, _ := res.Buffers()
	clients, _ := list.At(0).Clients()
	answered := 0
	for i := 0; i < clients.Len(); i++ {
		if clients.At(i).Answered() {
			answered++
		}
	}
	if answered != 2 {
		t.Errorf("%d clients answered, want 2 — the responsive ones must not be dragged down", answered)
	}
}

// TestCheckBufferConsistencyBoundsAStoppedClient covers the case a per-call
// context cannot handle: a client whose process is stopped, so no handler ever
// runs and nothing observes the cancellation. Verified against this capnp
// version, cancelling the caller's context does not resolve such a future, so
// the fan-out's own collection deadline is the only thing that bounds the wait.
// Without it a consistency check hangs exactly when a window is wedged — which
// is the situation it exists to diagnose.
func TestCheckBufferConsistencyBoundsAStoppedClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	sum := sha256.Sum256([]byte("x\n"))

	stopped := &reportingCallback{known: true, sum: sum[:], block: make(chan struct{}), ignoreCtx: true}
	defer close(stopped.block)
	responsive := &reportingCallback{known: true, sum: sum[:]}

	cbS := proto.ClientCallback_ServerToClient(stopped)
	defer cbS.Release()
	cbR := proto.ClientCallback_ServerToClient(responsive)
	defer cbR.Release()

	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "x\n"), clients: map[uint64]struct{}{1: {}, 2: {}}},
		},
		clientMap: map[uint64]*clientEntry{
			1: {callback: cbS},
			2: {callback: cbR},
		},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	done := make(chan struct{})
	var answered int
	go func() {
		defer close(done)
		fut, rel := cl.CheckBufferConsistency(context.Background(), func(p proto.EditorService_checkBufferConsistency_Params) error {
			p.SetBufferId(1)
			return nil
		})
		defer rel()
		res, err := fut.Struct()
		if err != nil {
			t.Errorf("CheckBufferConsistency: %v", err)
			return
		}
		list, _ := res.Buffers()
		clients, _ := list.At(0).Clients()
		for i := 0; i < clients.Len(); i++ {
			if clients.At(i).Answered() {
				answered++
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("CheckBufferConsistency never returned with a client that never runs its handler")
	}
	if answered != 1 {
		t.Errorf("%d clients answered, want 1 — the responsive one must still be reported", answered)
	}
}
