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

// handlerDone wraps connSvc to report when CheckBufferConsistency's handler
// itself returns. The caller's future is no evidence: an in-process caller
// that cancels can see its call fail at once while the handler goes on running.
type handlerDone struct {
	*connSvc
	returned chan time.Time
}

func (h *handlerDone) CheckBufferConsistency(ctx context.Context, call proto.EditorService_checkBufferConsistency) error {
	err := h.connSvc.CheckBufferConsistency(ctx, call)
	h.returned <- time.Now()
	return err
}

// TestCheckBufferConsistencyStopsWhenTheCallerGoesAway guards the ctx.Done case
// in the collect loop, which shipped without a test. The collect deadline is
// per buffer, so without it a caller that gave up still had the handler wait
// out that deadline once for every buffer, here 8 × 750ms, holding a goroutine
// per stopped client the whole time.
func TestCheckBufferConsistencyStopsWhenTheCallerGoesAway(t *testing.T) {
	dir := t.TempDir()
	sum := sha256.Sum256([]byte("x\n"))
	// A client whose process is stopped: its handler never runs, so only the
	// server's own bounds end the wait.
	stopped := &reportingCallback{known: true, sum: sum[:], block: make(chan struct{}), ignoreCtx: true}
	t.Cleanup(func() { close(stopped.block) })
	cb := proto.ClientCallback_ServerToClient(stopped)
	defer cb.Release()

	const buffers = 8
	s := &editorService{
		cfg:        &config.Config{},
		buffers:    map[uint32]*bufferEntry{},
		clientMap:  map[uint64]*clientEntry{1: {callback: cb}},
		dirWatches: make(map[string]int),
	}
	for i := uint32(1); i <= buffers; i++ {
		path := filepath.Join(dir, string(rune('a'+i))+".go")
		s.buffers[i] = &bufferEntry{buf: document.New(path, "x\n"), clients: map[uint64]struct{}{1: {}}}
	}
	h := &handlerDone{connSvc: &connSvc{editorService: s, connID: 1}, returned: make(chan time.Time, 1)}
	cl := proto.EditorService_ServerToClient(h)
	defer cl.Release()

	ctx, cancel := context.WithCancel(context.Background())
	fut, rel := cl.CheckBufferConsistency(ctx, func(p proto.EditorService_checkBufferConsistency_Params) error {
		p.SetBufferId(0) // every buffer
		return nil
	})
	time.Sleep(100 * time.Millisecond) // into the first buffer's wait
	cancelled := time.Now()
	cancel()
	go func() {
		fut.Struct() //nolint:errcheck
		rel()
	}()

	select {
	case at := <-h.returned:
		if waited := at.Sub(cancelled); waited > time.Second {
			t.Errorf("the handler ran %v after its caller cancelled; it must stop at once", waited)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never returned")
	}
}
