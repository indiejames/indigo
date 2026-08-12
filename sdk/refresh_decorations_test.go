package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// fakeEditorApi is a partial pluginproto.EditorApi_Server: only
// RefreshDecorations is implemented, every other method would nil-pointer
// panic if called (none are, by this test).
type fakeEditorApi struct {
	pluginproto.EditorApi_Server
	called  chan struct{}
	release chan struct{}
}

func (f *fakeEditorApi) RefreshDecorations(_ context.Context, call pluginproto.EditorApi_refreshDecorations) error {
	close(f.called)
	<-f.release // held open until the test releases it, simulating a slow/hung server
	_, err := call.AllocResults()
	return err
}

// TestRefreshDecorationsDoesNotBlockCaller is a regression test: the doc
// comment promises "fire-and-forget", but the old implementation blocked
// synchronously on fut.Struct() with no timeout, so a slow or wedged
// connection could hang the plugin's calling goroutine indefinitely.
func TestRefreshDecorationsDoesNotBlockCaller(t *testing.T) {
	fake := &fakeEditorApi{called: make(chan struct{}), release: make(chan struct{})}
	defer close(fake.release) // let the fake's handler return before the test exits

	client := pluginproto.EditorApi_ServerToClient(fake)
	defer client.Release()
	api := &Api{api: client}

	start := time.Now()
	api.RefreshDecorations(42)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("RefreshDecorations took %v — it should return immediately without waiting for the server", elapsed)
	}

	// The call must still actually reach the server in the background.
	select {
	case <-fake.called:
	case <-time.After(2 * time.Second):
		t.Fatal("RefreshDecorations never reached the server")
	}
}
