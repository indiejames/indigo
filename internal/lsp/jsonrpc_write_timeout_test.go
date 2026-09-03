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
//
// Call runs in its own goroutine, reporting back over a channel, rather
// than being called directly on the test goroutine: if this regresses and
// Call blocks forever, a fixed watchdog below fails fast with a clear
// message instead of leaving the whole test binary hung until its own
// top-level timeout. Assertions only ever run on the test goroutine
// (never inside the spawned one) so there's no risk of a t.Errorf/Fatal
// call racing — or firing after — this function's return. The test
// closes both pipe ends before returning specifically to unblock the
// leaked internal write goroutine Call spawned (still stuck on the hung
// Write), so it doesn't outlive this test.
func TestCallReturnsPromptlyWhenWriteHangs(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()

	conn := newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	type callResult struct {
		err     error
		elapsed time.Duration
	}
	callDone := make(chan callResult, 1)
	start := time.Now()
	go func() {
		_, err := conn.Call(ctx, "textDocument/hover", struct{}{})
		callDone <- callResult{err: err, elapsed: time.Since(start)}
	}()

	var res callResult
	select {
	case res = <-callDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Call() never returned — write() hanging forever without the fix")
	}

	if !errors.Is(res.err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", res.err)
	}
	// Fixed, this returns once ctx's 100ms deadline elapses; broken, it
	// would hang until the watchdog above fires (write() never returns
	// since nothing ever reads serverEnd).
	if res.elapsed > 500*time.Millisecond {
		t.Errorf("Call took %v, want it to return promptly once ctx's deadline elapsed (write() hanging forever without the fix)", res.elapsed)
	}

	// Unblock the leaked write goroutine now that the assertions above are
	// done, so it doesn't stay blocked for the rest of the test binary's
	// run.
	clientEnd.Close() //nolint:errcheck
	serverEnd.Close() //nolint:errcheck
}
