package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
)

// Save, SaveAs, :wq and Format are each their own command, and edits reach
// the server through the send queue's drain, another command. Nothing used to
// order the two, so a save could be written before the last keystrokes
// arrived — and with :wq, CloseBuffer then freed the buffer and those
// keystrokes were gone. These tests hold the drain back, the way a slow
// server does, and check what reaches disk.

func openOneClient(t *testing.T) (*otClient, string) {
	t.Helper()
	workDir, sock := startOTServer(t)
	path := filepath.Join(workDir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dialOTClient(t, sock, path, workDir, "A"), path
}

// runAsync runs a command on its own goroutine, as Bubble Tea would.
func runAsync(cmd func() any) <-chan any {
	out := make(chan any, 1)
	go func() { out <- cmd() }()
	return out
}

func notYet(t *testing.T, ch <-chan any, what string) {
	t.Helper()
	select {
	case msg := <-ch:
		t.Fatalf("%s finished before the queued edit was sent: %#v", what, msg)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitMsg(t *testing.T, ch <-chan any) any {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("command never finished")
		return nil
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSaveWaitsForQueuedEdits(t *testing.T) {
	a, path := openOneClient(t)
	a.edit(t, otInsert(0, 5, " world")) // queued, drain held back

	cmd := a.m.doSaveNow()
	done := runAsync(func() any { return cmd() })
	notYet(t, done, "Save")

	a.flush(t)
	if msg, ok := waitMsg(t, done).(savedMsg); !ok {
		t.Fatalf("save result = %#v, want savedMsg", msg)
	}
	if got := readFile(t, path); got != "hello world\n" {
		t.Fatalf("saved %q, want the queued edit included", got)
	}
}

func TestSaveAndCloseWaitsForQueuedEdits(t *testing.T) {
	a, path := openOneClient(t)
	a.edit(t, otInsert(0, 5, " world"))

	cmd := a.m.doSaveAndClose()
	done := runAsync(func() any { return cmd() })
	notYet(t, done, ":wq")

	a.flush(t)
	if msg, ok := waitMsg(t, done).(CloseBufferMsg); !ok {
		t.Fatalf(":wq result = %#v, want CloseBufferMsg", msg)
	}
	if got := readFile(t, path); got != "hello world\n" {
		t.Fatalf(":wq saved %q, want the queued edit included", got)
	}
}

func TestFormatWaitsForQueuedEdits(t *testing.T) {
	a, _ := openOneClient(t)
	a.edit(t, otInsert(0, 5, " world"))

	cmd := a.m.fetchFormat(false)
	done := runAsync(func() any { return cmd() })
	notYet(t, done, "Format")
	a.flush(t)
	waitMsg(t, done)
}

// A save whose edits were discarded (a failed send, a resync) must not go
// ahead: it would write content without them and report success.
func TestSaveRefusesWhenQueuedEditsAreDiscarded(t *testing.T) {
	a, path := openOneClient(t)
	a.edit(t, otInsert(0, 5, " world"))

	cmd := a.m.doSaveNow()
	done := runAsync(func() any { return cmd() })
	notYet(t, done, "Save")

	a.m.sendQ.discard()
	msg, ok := waitMsg(t, done).(saveFailedMsg)
	if !ok || !errors.Is(msg.err, errEditsNotSent) {
		t.Fatalf("save result = %#v, want saveFailedMsg with errEditsNotSent", msg)
	}
	if got := readFile(t, path); got != "hello\n" {
		t.Fatalf("file = %q, want it untouched", got)
	}
}

// --- resync ---------------------------------------------------------------

// assertMatchesServer checks the window shows exactly what the server holds,
// after giving both every chance to catch up.
func assertMatchesServer(t *testing.T, c *otClient) {
	t.Helper()
	c.settle(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, _, _, _, err := c.rpc.GetBufferSnapshot(ctx, c.m.bufID)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.m.buf.Content(); got != server {
		t.Fatalf("window shows %q but the server holds %q", got, server)
	}
}

func adoptResync(t *testing.T, c *otClient, msg any) {
	t.Helper()
	if rm, ok := msg.(bufferResyncMsg); !ok || rm.err != nil {
		t.Fatalf("resync returned %#v", msg)
	}
	updated, cmd := c.m.Update(msg)
	c.m = updated.(Model)
	if cmd != nil {
		c.runCmd(t, func() any { return cmd() })
	}
}

// An edit queued but not yet sent when a resync starts used to be sent anyway,
// after the snapshot was taken. The server applied it; the window adopted a
// snapshot without it and, since the server does not echo a sender's own ops,
// never saw it.
func TestResyncDoesNotSendEditsQueuedBeforeIt(t *testing.T) {
	a, _ := openOneClient(t)
	a.edit(t, otInsert(0, 0, "X"))

	resync := a.m.resyncFromServer("")
	msg := resync()
	a.flush(t) // the drain that was already scheduled runs late
	adoptResync(t, a, msg)

	assertMatchesServer(t, a)
}

// Keystrokes typed while the snapshot is in flight are held, not sent.
func TestResyncHoldsEditsTypedDuringIt(t *testing.T) {
	a, _ := openOneClient(t)

	resync := a.m.resyncFromServer("")
	msg := resync()
	a.edit(t, otInsert(0, 0, "Y")) // typed during the round trip
	a.flush(t)
	adoptResync(t, a, msg)

	assertMatchesServer(t, a)
	if !a.m.sendQ.idle() {
		t.Fatal("send queue still paused after the resync was adopted")
	}
}

// An op already popped and on its way to the server when the resync starts
// cannot be recalled, so the snapshot has to wait for it.
func TestResyncWaitsForAnInFlightEdit(t *testing.T) {
	a, _ := openOneClient(t)
	a.edit(t, otInsert(0, 0, "X"))
	a.drain = nil // stand in for the drain by hand, to hold it mid-send

	s, ok := a.m.sendQ.next(a.m.sendQ.currentEpoch())
	if !ok {
		t.Fatal("nothing queued")
	}
	resync := a.m.resyncFromServer("")
	done := runAsync(func() any { return resync() })
	notYet(t, done, "the resync snapshot")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := a.rpc.ApplyOp(ctx, s.bufID, s.op, s.generation, s.baseVersion); err != nil {
		t.Fatal(err)
	}
	a.m.sendQ.sent()

	adoptResync(t, a, waitMsg(t, done))
	assertMatchesServer(t, a)
	if got := a.m.buf.Content(); got != "Xhello\n" {
		t.Fatalf("window = %q, want the in-flight edit in the adopted snapshot", got)
	}
}

// A resync that fails has no snapshot to adopt, so the edits held during it
// are sent after all — otherwise the buffer would stop syncing for good.
func TestFailedResyncReleasesHeldEdits(t *testing.T) {
	a, _ := openOneClient(t)

	a.m.sendQ.pauseForResync()
	a.edit(t, otInsert(0, 0, "Y"))
	if a.drain != nil {
		t.Fatal("an edit was sent while paused")
	}
	updated, cmd := a.m.Update(bufferResyncMsg{bufID: a.m.bufID, err: errors.New("boom")})
	a.m = updated.(Model)
	if cmd == nil {
		t.Fatal("failed resync did not restart sending")
	}
	a.runCmd(t, func() any { return cmd() })
	assertMatchesServer(t, a)
	if got := a.m.buf.Content(); got != "Yhello\n" {
		t.Fatalf("window = %q", got)
	}
}

// --- queue mechanics ------------------------------------------------------

func TestSendQueueBarrier(t *testing.T) {
	var q sendQueue
	if ok := <-q.barrier(); !ok {
		t.Fatal("barrier on an idle queue should release at once")
	}

	_, epoch := q.enqueue(queuedSend{op: document.Op{Type: document.OpInsert}})
	b := q.barrier()
	q.enqueue(queuedSend{op: document.Op{Type: document.OpInsert}}) // after: must not hold it
	select {
	case <-b:
		t.Fatal("released before the op ahead of it was sent")
	default:
	}
	if _, ok := q.next(epoch); !ok {
		t.Fatal("expected the first op")
	}
	q.sent()
	select {
	case <-b:
		t.Fatal("released before the drain moved past the first op")
	default:
	}
	if _, ok := q.next(epoch); !ok { // reaching the second op passes the barrier
		t.Fatal("expected the second op")
	}
	select {
	case ok := <-b:
		if !ok {
			t.Fatal("barrier released false after a successful send")
		}
	default:
		t.Fatal("barrier not released once the ops ahead of it were sent")
	}

	b2 := q.barrier()
	q.discard()
	if ok := <-b2; ok {
		t.Fatal("barrier released true for ops that were discarded")
	}
}
