package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

// TestCallFailsFastWhenConnectionCloses is a regression test: when the
// language server process dies mid-request (its stdout pipe hits EOF),
// readLoop used to return silently, leaving the in-flight Call() blocked
// until its own context deadline instead of failing immediately. In
// production this showed up as "initialize response: err=context deadline
// exceeded" in the LSP log — a 30-second wait — even though the server
// process had already exited. See failPending in jsonrpc.go.
func TestCallFailsFastWhenConnectionCloses(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck

	// A fake server that reads (and thus unblocks the client's write) but
	// never replies — simulating a process that started, accepted the
	// request, then died before responding.
	received := make(chan struct{})
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, _ json.RawMessage) (any, error) {
		close(received)
		select {} // never respond
	})
	_ = fakeServer

	conn := newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	callDone := make(chan error, 1)
	start := time.Now()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := conn.Call(ctx, "initialize", struct{}{})
		callDone <- err
	}()

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("fake server never received the request")
	}

	// The request is now genuinely pending. Kill the "process".
	serverEnd.Close() //nolint:errcheck

	var err error
	select {
	case err = <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Call() never returned after the connection closed — readLoop's EOF didn't wake it")
	}
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Call() succeeded after the connection closed, want an error")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Call() returned context.DeadlineExceeded — it waited out the full ctx timeout " +
			"instead of being woken by readLoop's EOF")
	}
	if elapsed > time.Second {
		t.Fatalf("Call() took %v to fail after the connection closed — want near-instant", elapsed)
	}
}
