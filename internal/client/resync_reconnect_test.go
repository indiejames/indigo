package client

import (
	"errors"
	"testing"
)

// A resync's result that belongs to a connection since replaced must not touch
// the buffer. The test model has no RPC, so its connection is reconnect 0, and
// a result stamped with any other number came from a different one.

// TestASupersededResyncFailureIsIgnored: a resync started on the old
// connection fails once that connection is gone, typically after the buffer
// has already reattached to the new one. Treated as a fresh drop it detached
// the buffer, and with no further reconnect coming nothing ever resumed it.
func TestASupersededResyncFailureIsIgnored(t *testing.T) {
	m := newTestModel("reattached\n")
	m.bufID = 1
	updated, cmd := m.Update(bufferResyncMsg{bufID: 1, seq: 1, err: errors.New("rpc: connection closed")})
	got := updated.(Model)
	if got.detached {
		t.Error("a reattached buffer was detached by a failure from the connection it replaced")
	}
	if got.severeErr != "" {
		t.Errorf("reported a failure of a superseded connection: %q", got.severeErr)
	}
	if !got.sendQ.idle() {
		t.Error("the send queue was left paused")
	}
	if cmd != nil {
		t.Error("a superseded failure started a command")
	}
}

// TestASupersededResyncSnapshotIsNotAdopted: a snapshot from the old server
// would replace the content the reattach established on the new one.
func TestASupersededResyncSnapshotIsNotAdopted(t *testing.T) {
	m := newTestModel("reattached\n")
	m.bufID = 1
	updated, _ := m.Update(bufferResyncMsg{bufID: 1, seq: 1, content: "old server\n", version: 9})
	if got := updated.(Model).buf.Content(); got != "reattached\n" {
		t.Errorf("content = %q; a snapshot from a replaced connection was adopted", got)
	}
}

// TestAResyncSnapshotIsNotAdoptedWhileDetached: the reattach that follows
// settles the buffer against the new server. Adopting first would discard the
// window's edits that the reattach is about to send.
func TestAResyncSnapshotIsNotAdoptedWhileDetached(t *testing.T) {
	m := reattachFixture("one\nedited\n", true)
	updated, _ := m.Update(bufferResyncMsg{bufID: 1, content: "one\n", version: 4})
	got := updated.(Model)
	if c := got.buf.Content(); c != "one\nedited\n" {
		t.Errorf("content = %q; a snapshot was adopted while detached", c)
	}
	if !got.detached {
		t.Error("no longer detached")
	}
}

// TestACurrentResyncSnapshotIsStillAdopted: the guard must not swallow the
// ordinary case.
func TestACurrentResyncSnapshotIsStillAdopted(t *testing.T) {
	m := newTestModel("local\n")
	m.bufID = 1
	updated, _ := m.Update(bufferResyncMsg{bufID: 1, seq: 0, content: "server\n", version: 3})
	if got := updated.(Model).buf.Content(); got != "server\n" {
		t.Errorf("content = %q, want the server's snapshot", got)
	}
}
