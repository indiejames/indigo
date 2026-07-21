package lsp

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

// TestDidChangeConcurrentCallsPreserveVersionOrder is a regression test for a
// race where concurrent DidChange calls could reach the server with their
// assigned versions out of order (an LSP protocol violation): the old code
// released the client's lock after bumping the document version but before
// writing the notification, so a goroutine holding a later version could win
// the race to write before a goroutine holding an earlier one. A server
// (gopls included) that receives version N+1 before version N ends up with
// its "current" snapshot pinned to the older content, so any request sent
// right after — like a rename immediately following an edit — can silently
// be computed against stale content.
func TestDidChangeConcurrentCallsPreserveVersionOrder(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	var mu sync.Mutex
	var versions []int
	received := make(chan struct{}, 200)

	fakeServer := newJSONRPCConn(serverEnd, serverEnd, func(method string, params json.RawMessage) {
		if method != "textDocument/didChange" {
			return
		}
		var p DidChangeTextDocumentParams
		if err := json.Unmarshal(params, &p); err != nil {
			t.Errorf("unmarshal didChange params: %v", err)
			return
		}
		mu.Lock()
		versions = append(versions, p.TextDocument.Version)
		mu.Unlock()
		received <- struct{}{}
	}, nil)
	_ = fakeServer // its background readLoop stops when serverEnd is closed above

	c := &Client{docVersions: make(map[string]int)}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := c.DidChange("/a.go", "content"); err != nil {
				t.Errorf("DidChange: %v", err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		select {
		case <-received:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for notification %d/%d", i+1, n)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(versions) != n {
		t.Fatalf("received %d notifications, want %d", len(versions), n)
	}
	for i, v := range versions {
		if v != i+1 {
			t.Fatalf("versions out of order: %v (first mismatch at index %d: got %d, want %d)", versions, i, v, i+1)
		}
	}
}

// TestRenameAfterChangeIsAtomicWithConcurrentDidChange is a regression test
// for a bug where a rename issued immediately after an edit (e.g. Extract
// Function's automatic follow-up rename) could get gopls's response
// "no identifier found" for a position that was, in fact, exactly on the
// target identifier: DidChange and the subsequent Rename call were two
// separate lock acquisitions, leaving a gap in between where some other,
// unrelated DidChange call for the same client (most plausibly a
// slow-to-schedule goroutine left over from the edit's own ApplyOp calls)
// could slip in and invalidate the fresh snapshot the rename depended on.
// RenameAfterChange holds the client's lock across the whole notify+rename
// round trip, so nothing else can interleave.
func TestRenameAfterChangeIsAtomicWithConcurrentDidChange(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	var mu sync.Mutex
	var events []string

	fakeServer := newJSONRPCConn(serverEnd, serverEnd,
		func(method string, params json.RawMessage) {
			if method != "textDocument/didChange" {
				return
			}
			var p DidChangeTextDocumentParams
			if err := json.Unmarshal(params, &p); err != nil {
				t.Errorf("unmarshal didChange params: %v", err)
				return
			}
			mu.Lock()
			events = append(events, "didChange")
			mu.Unlock()
		},
		func(method string, _ json.RawMessage) (any, error) {
			if method != "textDocument/rename" {
				return nil, nil
			}
			mu.Lock()
			events = append(events, "rename-start")
			mu.Unlock()
			time.Sleep(100 * time.Millisecond) // simulate a slow rename computation
			mu.Lock()
			events = append(events, "rename-end")
			mu.Unlock()
			return WorkspaceEdit{}, nil
		},
	)
	_ = fakeServer

	c := &Client{
		docVersions: make(map[string]int),
		caps:        ServerCapabilities{RenameProvider: true},
	}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := c.RenameAfterChange("/a.go", "content", 0, 0, "newName"); err != nil {
			t.Errorf("RenameAfterChange: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond) // let RenameAfterChange start first
		if err := c.DidChange("/a.go", "content2"); err != nil {
			t.Errorf("DidChange: %v", err)
		}
	}()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	// Expect: RenameAfterChange's own didChange, then rename-start, then
	// rename-end, then the concurrent goroutine's didChange — never a
	// didChange sandwiched between rename-start and rename-end.
	if len(events) != 4 {
		t.Fatalf("events = %v, want 4 entries", events)
	}
	if events[0] != "didChange" || events[1] != "rename-start" || events[2] != "rename-end" || events[3] != "didChange" {
		t.Errorf("events out of order: %v, want [didChange rename-start rename-end didChange]", events)
	}
}
