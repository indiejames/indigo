package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/rpcclient"
)

// reattachFixture is a buffer that was showing local when its connection
// dropped, with one edit that had not reached the server. dirty says whether
// it had unsaved changes.
func reattachFixture(local string, dirty bool) Model {
	m := newTestModel(local)
	m.bufID = 1
	m.filePath = "test.go"
	if dirty {
		m.buf.MarkDirty()
	}
	m.undoStack = []undoEntry{{}}
	m.pending = []pendingOp{{seq: 1}}
	m.nextSeq = 1
	return m.Detach()
}

func reattach(t *testing.T, m Model, res rpcclient.Reattached) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := m.handleReattached(reattachedMsg{bufID: m.bufID, res: res})
	if m.detached {
		t.Error("still detached after reattaching")
	}
	return m, cmd
}

// TestReattachingKeepsTheWindowsEditsWhenTheServerLacksThem: the usual case. The
// server is a new one that read the recovery file its predecessor wrote, which
// lacks the edit still on its way when the connection dropped. The window's
// text stays, undo history included, and the server is sent what it lacks.
func TestReattachingKeepsTheWindowsEditsWhenTheServerLacksThem(t *testing.T) {
	const server, local = "one\ntwo\n", "one\ntwo!\n"
	m := reattachFixture(local, true)
	m, _ = reattach(t, m, rpcclient.Reattached{Content: server, Version: 4, FromRecovery: true})

	if got := m.buf.Content(); got != local {
		t.Errorf("window content = %q, want its own %q", got, local)
	}
	if len(m.undoStack) != 1 {
		t.Errorf("undo history has %d entries, want the 1 it had: the text did not change", len(m.undoStack))
	}
	if m.version != 4 {
		t.Errorf("version = %d, want the new server's 4", m.version)
	}
	// What was pending went to the old server; what is pending now must turn
	// the new server's text into the window's.
	doc := document.New("test.go", server)
	for _, p := range m.pending {
		for _, op := range p.ops {
			doc.Apply(op)
		}
	}
	if got := doc.Content(); got != local {
		t.Errorf("the edits sent would leave the server with %q, want %q", got, local)
	}
	if m.pending[0].seq != 1 {
		t.Errorf("first op numbered %d, want 1: the new server counts this client's ops from scratch", m.pending[0].seq)
	}
}

// TestReattachingTakesTheServersCopyWhenAnotherWindowHasTheBuffer: the other
// window kept editing while this one was away, and those edits are only in the
// server's copy.
func TestReattachingTakesTheServersCopyWhenAnotherWindowHasTheBuffer(t *testing.T) {
	const server, local = "one\nedited elsewhere\n", "one\ntwo!\n"
	m := reattachFixture(local, true)
	m, _ = reattach(t, m, rpcclient.Reattached{Content: server, Version: 9, Shared: true})

	if got := m.buf.Content(); got != server {
		t.Errorf("window content = %q, want the server's %q", got, server)
	}
	if len(m.pending) != 0 {
		t.Errorf("%d edit(s) pending, want none: the server's copy was taken as it is", len(m.pending))
	}
	if !m.buf.Dirty() {
		t.Error("buffer is clean; the server's copy may be unsaved, so it must show as dirty")
	}
}

// TestReattachingReloadsACleanBufferWhoseFileChanged: nothing unsaved here, and
// the file changed on disk while the window was away, so the buffer follows the
// file as it would have had the window been connected.
func TestReattachingReloadsACleanBufferWhoseFileChanged(t *testing.T) {
	const disk, local = "changed on disk\n", "one\ntwo\n"
	m := reattachFixture(local, false)
	m, _ = reattach(t, m, rpcclient.Reattached{Content: disk, Version: 1})

	if got := m.buf.Content(); got != disk {
		t.Errorf("window content = %q, want the file's %q", got, disk)
	}
	if m.buf.Dirty() {
		t.Error("buffer is dirty, but it holds exactly what is on disk")
	}
}

// TestReattachingChangesNothingWhenTheCopiesAgree: the common case after a
// drop with nothing in flight.
func TestReattachingChangesNothingWhenTheCopiesAgree(t *testing.T) {
	const text = "one\ntwo\n"
	m := reattachFixture(text, true)
	m, cmd := reattach(t, m, rpcclient.Reattached{Content: text, Version: 2, Generation: 3})

	if len(m.pending) != 0 || len(m.undoStack) != 1 {
		t.Errorf("pending=%d undo=%d, want 0 and 1", len(m.pending), len(m.undoStack))
	}
	if m.version != 2 || m.generation != 3 {
		t.Errorf("version/generation = %d/%d, want the new server's 2/3", m.version, m.generation)
	}
	if done, ok := cmd().(ReattachedMsg); !ok || done.BufID != 1 || done.Err != nil {
		t.Errorf("command produced %#v, want ReattachedMsg for buffer 1 with no error", cmd())
	}
}

// TestADetachedBufferDoesNotPoll: polling waits for the send queue to be idle,
// so Detach's pause is what stops it. A poll that reached the new server after
// the buffer was bound there, but before the window took in the reattach,
// would report that server's generation; the mismatch starts a resync, and the
// resync replaces the window's text with the server's, losing the edits the
// reattach was about to send.
func TestADetachedBufferDoesNotPoll(t *testing.T) {
	m := newTestModel("one\n")
	if !m.sendQ.idle() {
		t.Fatal("fixture's queue is not idle to begin with")
	}
	if m.Detach().sendQ.idle() {
		t.Error("a detached buffer's queue is idle, so it would go on polling")
	}
}

// TestADetachedBufferDoesNotResyncAgainstTheDeadConnection: an edit that
// failed because the connection dropped stays in the buffer for reattaching to
// reconcile, instead of starting a resync that can only fail.
func TestADetachedBufferDoesNotResyncAgainstTheDeadConnection(t *testing.T) {
	m := reattachFixture("one\n", true)
	next, cmd := m.Update(applyOpFailedMsg{bufID: 1})
	if cmd != nil {
		t.Error("a failed send while detached started a command (a resync)")
	}
	if !next.(Model).detached {
		t.Error("no longer detached")
	}
}
