package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// These cover a window that starts its op stream over while the server keeps
// the buffer open. The client restarts nextSeq at 0 whenever it does that, and
// retires pending ops whose number is at or below the server's count of ops
// applied from it (appliedFromCaller). The server used to reset that count only
// on a wholesale buffer swap or a disconnect, so after a close-and-reopen or a
// plain resync the count stayed ahead. The window then retired new ops the
// server had never received, stopped rebasing incoming ops past them, and
// diverged for good with no error — and its later edits landed at the wrong
// coordinates on the server, so the corruption reached disk.
//
// The shape both tests drive: A edits once so the server's count for A is
// non-zero, A starts over, B edits, and A polls with an edit of its own still
// unsent — the one moment the client-side rebase has work to do.

func otInsert(line, col int, text string) document.Op {
	return document.Op{Type: document.OpInsert, InsertLine: line, InsertCol: col, InsertText: text}
}

func setupRestartScenario(t *testing.T) (a, b *otClient, path, workDir string) {
	t.Helper()
	workDir, sock := startOTServer(t)
	path = filepath.Join(workDir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a = dialOTClient(t, sock, path, workDir, "A")
	b = dialOTClient(t, sock, path, workDir, "B")

	a.edit(t, otInsert(0, 0, "X"))
	a.flush(t)
	a.poll(t)
	b.settle(t)
	return a, b, path, workDir
}

func assertConverged(t *testing.T, clients ...*otClient) {
	t.Helper()
	for _, c := range clients {
		c.settle(t)
	}
	want := clients[0].m.buf.Content()
	for _, c := range clients[1:] {
		if got := c.m.buf.Content(); got != want {
			t.Fatalf("clients diverged\n  %s: %q\n  %s: %q", clients[0].name, want, c.name, got)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, _, _, _, err := clients[0].rpc.GetBufferSnapshot(ctx, clients[0].m.bufID)
	if err != nil {
		t.Fatalf("GetBufferSnapshot: %v", err)
	}
	if server != want {
		t.Fatalf("clients agree on %q but the server holds %q", want, server)
	}
}

// TestReopenWhileAnotherWindowHoldsBufferConverges: a window closes a file's
// tab and reopens it while another window keeps it open, so the buffer (and,
// before the fix, the server's applied count for the window) survives.
func TestReopenWhileAnotherWindowHoldsBufferConverges(t *testing.T) {
	a, b, path, workDir := setupRestartScenario(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.rpc.CloseBuffer(ctx, a.m.bufID); err != nil {
		t.Fatalf("CloseBuffer: %v", err)
	}
	bufID, content, version, fromRecovery, generation, err := a.rpc.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if bufID != b.m.bufID {
		t.Fatalf("reopen got buffer %d, want the one B still holds (%d)", bufID, b.m.bufID)
	}
	a2 := &otClient{
		m:    New(a.rpc, bufID, content, version, path, workDir, &config.Config{}, fromRecovery, generation),
		rpc:  a.rpc,
		name: "A2",
	}

	b.edit(t, otInsert(0, 5, "Y"))
	b.flush(t)
	a2.pollWithEditInFlight(t, otInsert(0, 0, "Z"))
	a2.flush(t)

	assertConverged(t, a2, b)
}

// TestResyncWithoutBufferSwapConverges: a resync with no generation bump —
// what a failed send produces — restarts the window's op numbering just as a
// reopen does.
func TestResyncWithoutBufferSwapConverges(t *testing.T) {
	a, b, _, _ := setupRestartScenario(t)

	msg := a.m.resyncFromServer("")()
	if rm, ok := msg.(bufferResyncMsg); !ok || rm.err != nil {
		t.Fatalf("resync returned %#v", msg)
	}
	updated, _ := a.m.Update(msg)
	a.m = updated.(Model)
	if a.m.nextSeq != 0 {
		t.Fatalf("nextSeq = %d after resync, want 0", a.m.nextSeq)
	}

	b.edit(t, otInsert(0, 5, "Y"))
	b.flush(t)
	a.pollWithEditInFlight(t, otInsert(0, 0, "Z"))
	a.flush(t)

	assertConverged(t, a, b)
}
