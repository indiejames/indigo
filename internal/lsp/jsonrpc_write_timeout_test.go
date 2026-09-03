package lsp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// TestCallReturnsPromptlyWhenWriteHangs is a regression test for deferred
// hardening flagged during the RPC head-of-line-blocking fix (see
// PLAN.md): Call's ctx-aware select only ran *after* write() returned, so
// a write that blocks (e.g. a language server subprocess not draining its
// stdin, or waiting on wMu behind another write stuck the same way) made
// the caller's configured timeout meaningless — Call would hang for as
// long as the write stayed blocked, not the timeout the caller asked for.
//
// net.Pipe() is unbuffered/synchronous: a Write on one end blocks until a
// Read happens on the other. Nothing here ever reads serverEnd, so
// clientEnd's Write (inside jsonrpcConn.write) blocks forever, exactly
// simulating a stuck language server — no custom blocking io.Writer
// needed.
func TestCallReturnsPromptlyWhenWriteHangs(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	// Closing either end unblocks the leaked write() goroutine (from the
	// fix, still trying to write in the background after Call returns) so
	// it doesn't stay blocked for the rest of the test binary's run.
	defer serverEnd.Close() //nolint:errcheck
	defer clientEnd.Close() //nolint:errcheck

	conn := newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := conn.Call(ctx, "textDocument/hover", struct{}{})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	// Fixed, this returns once ctx's 100ms deadline elapses; broken, it
	// would hang until the test's own timeout (write() never returns since
	// nothing ever reads serverEnd).
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Call took %v, want it to return promptly once ctx's deadline elapsed (write() hanging forever without the fix)", elapsed)
	}
}
