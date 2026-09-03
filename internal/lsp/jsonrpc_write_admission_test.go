package lsp

import (
	"context"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

// writerFunc adapts a function to an io.Writer, for a test double whose
// Write behavior needs to be defined inline.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestCallBoundsWriteGoroutinesUnderSustainedTimeouts is a regression test
// for a goroutine-accumulation gap in the write-timeout fix (see
// TestCallReturnsPromptlyWhenWriteHangs and PLAN.md): every Call() against a
// permanently stuck connection used to spawn its own goroutine to run
// write(), and while write()'s own wMu mutex means only one of them is ever
// actually inside the underlying io.Writer.Write at a time, all the others
// still pile up as live goroutines blocked on that mutex forever — a real
// leak against a connection that stays stuck for a while, since automatic
// background polling (InlayHints, SemanticTokensRange) calls Call every few
// seconds per open buffer. Call now gates spawning that goroutine behind a
// size-1 semaphore (writeSem) acquired via a ctx-aware select, so only the
// first stuck write's goroutine is ever spawned at all — every subsequent
// timed-out Call against the same connection gives up at the
// semaphore-acquire step without spawning anything, blocked or not.
//
// This drives several concurrent Call attempts (all timing out) against a
// writer that blocks until explicitly released, then compares
// runtime.NumGoroutine() before and after: counting Write() entries alone
// can't distinguish the fixed and broken behavior (wMu already serializes
// entry into Write regardless of how many goroutines are queued up behind
// it), so this counts live goroutines instead.
func TestCallBoundsWriteGoroutinesUnderSustainedTimeouts(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close() //nolint:errcheck

	release := make(chan struct{})
	writeStarted := make(chan struct{}, 1)
	blockingWriter := writerFunc(func(p []byte) (int, error) {
		select {
		case writeStarted <- struct{}{}:
		default:
		}
		<-release
		return len(p), nil
	})

	conn := newJSONRPCConn(pr, blockingWriter, nil, nil)

	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			_, _ = conn.Call(ctx, "textDocument/hover", struct{}{})
		}()
	}

	select {
	case <-writeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("no write ever started — test setup is broken")
	}

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Call attempts never returned after their ctx timeouts elapsed")
	}

	// All n Call()s have returned, but the write goroutine(s) they may have
	// spawned are still blocked on release, which hasn't been closed yet —
	// this is exactly the moment to observe how many are actually alive.
	time.Sleep(20 * time.Millisecond)
	after := runtime.NumGoroutine()

	if leaked := after - before; leaked > 2 {
		t.Errorf("goroutine count grew by %d across %d concurrent timed-out Calls against a stuck connection "+
			"(before=%d after=%d), want at most ~1 leaked write goroutine — writeSem should have blocked the "+
			"rest from ever being spawned", leaked, n, before, after)
	}

	close(release)
}
