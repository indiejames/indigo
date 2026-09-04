package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// blockingBufferHandler is a BufferEventHandler whose OnOpen never answers on
// its own — it blocks until its context is cancelled, standing in for a
// hung or very slow plugin. It records when OnOpen was entered, when that
// context was cancelled, and when the capability itself was shut down (which
// capnp does once the last reference to it is released).
type blockingBufferHandler struct {
	entered  chan struct{}
	ctxDone  chan struct{}
	shutdown chan struct{}
	once     sync.Once
}

func (h *blockingBufferHandler) OnOpen(ctx context.Context, _ pluginproto.BufferEventHandler_onOpen) error {
	h.once.Do(func() { close(h.entered) })
	<-ctx.Done()
	close(h.ctxDone)
	return ctx.Err()
}

func (h *blockingBufferHandler) OnChange(context.Context, pluginproto.BufferEventHandler_onChange) error {
	return nil
}
func (h *blockingBufferHandler) OnSave(context.Context, pluginproto.BufferEventHandler_onSave) error {
	return nil
}
func (h *blockingBufferHandler) OnClose(context.Context, pluginproto.BufferEventHandler_onClose) error {
	return nil
}

// Shutdown satisfies capnp's server.Shutdowner: it runs once the capability's
// last reference is dropped, which is what makes the reference-leak assertion
// below observable.
func (h *blockingBufferHandler) Shutdown() { close(h.shutdown) }

// openBuffersOnlyBridge implements just the one ServerBridge method this code
// path uses. The embedded nil interface makes any other call panic loudly
// rather than silently returning zero values, so the test can't quietly drift
// into exercising something it doesn't model.
type openBuffersOnlyBridge struct {
	ServerBridge
	refs []PluginBufferRef
}

func (b openBuffersOnlyBridge) PluginOpenBuffers() []PluginBufferRef { return b.refs }

// TestRegisterBufferHandlerCatchUpDispatchIsBoundedAndReleases is a
// regression test for two defects in RegisterBufferHandler's catch-up OnOpen
// dispatch (the one that replays already-open buffers to a plugin that just
// registered a handler):
//
//   - It called OnOpen with a bare context.Background(), so a plugin that
//     never answers parked that goroutine forever. Every comparable dispatch
//     in this package is bounded (500ms for the decoration/action providers,
//     2s for the popup and prompt callbacks); this one was missed.
//   - It passed h.AddRef() into the goroutine and never released it — rel()
//     releases the *call*, not the capability — leaking one reference per
//     already-open buffer, per plugin. Compare GetActionsAt in manager.go,
//     which pairs its AddRef() with an explicit Release().
func TestRegisterBufferHandlerCatchUpDispatchIsBoundedAndReleases(t *testing.T) {
	h := &blockingBufferHandler{
		entered:  make(chan struct{}),
		ctxDone:  make(chan struct{}),
		shutdown: make(chan struct{}),
	}
	handlerClient := pluginproto.BufferEventHandler_ServerToClient(h)

	reg := &registeredPlugin{name: "test-plugin"}
	bridge := openBuffersOnlyBridge{refs: []PluginBufferRef{{BufID: 1, Path: "/tmp/already-open.go"}}}
	api := pluginproto.EditorApi_ServerToClient(&editorApiServer{reg: reg, bridge: bridge})
	defer api.Release()

	fut, rel := api.RegisterBufferHandler(context.Background(),
		func(p pluginproto.EditorApi_registerBufferHandler_Params) error {
			return p.SetHandler(handlerClient)
		})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("RegisterBufferHandler: %v", err)
	}

	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("catch-up OnOpen was never dispatched for the already-open buffer")
	}

	// The dispatch must be bounded: an unanswered call gets cancelled rather
	// than hanging its goroutine for the life of the process.
	select {
	case <-h.ctxDone:
	case <-time.After(10 * time.Second):
		t.Fatal("catch-up OnOpen was never cancelled — the dispatch has no timeout")
	}

	// ...and it must release its own reference. Drop the two references the
	// test and the registry hold; if the goroutine also released its
	// AddRef'd one, that's the last of them and the capability shuts down.
	handlerClient.Release()
	reg.mu.Lock()
	stored := reg.bufHandler
	reg.bufHandler = pluginproto.BufferEventHandler{}
	reg.mu.Unlock()
	stored.Release()

	select {
	case <-h.shutdown:
	case <-time.After(10 * time.Second):
		t.Fatal("handler capability never shut down — the catch-up dispatch leaked a reference")
	}
}
