package client

import (
	"errors"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

var errTest = errors.New("test error")

// TestSendToServerReturnsApplyOpFailedMsgOnError is a regression test:
// sendToServer previously returned a plain errorMsg on RPC failure, which
// just showed a status message and left the local buffer (which already
// applied the op) permanently diverged from the server with no recovery.
// It must now return applyOpFailedMsg so the caller can resync.
func TestSendToServerReturnsApplyOpFailedMsgOnError(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{} // zero-value RPC: the underlying capnp call fails immediately

	cmd := m.sendToServer(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
	if cmd == nil {
		t.Fatal("sendToServer returned a nil command")
	}
	msg := cmd()
	if _, ok := msg.(applyOpFailedMsg); !ok {
		t.Fatalf("sendToServer's command returned %T, want applyOpFailedMsg", msg)
	}
}

// TestApplyOpFailedMsgTriggersResync verifies the failure notice both shows
// a status message and kicks off a resync fetch.
func TestApplyOpFailedMsgTriggersResync(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}

	updated, cmd := m.Update(applyOpFailedMsg{err: errTest})
	m2 := updated.(Model)

	if m2.severeErr == "" {
		t.Error("expected a must-dismiss error modal after an ApplyOp failure")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil resync command")
	}
}

// TestBufferResyncMsgAppliesNewContent is a regression test for the actual
// recovery: a successful resync must replace the buffer content, adopt the
// server's version, discard local undo/redo history (whose inverse ops
// reference the pre-resync content and would corrupt the new buffer if
// replayed), and mark the buffer dirty (the server's in-memory content may
// itself not match disk, so err toward "unsaved" rather than a false-clean
// marker).
func TestBufferResyncMsgAppliesNewContent(t *testing.T) {
	m := newTestModel("old content\n")
	m.bufID = 7
	m.version = 1
	m.cursor = document.Pos{Line: 0, Col: 5}
	m.undoStack = []undoEntry{{}}
	m.redoStack = []undoEntry{{}}
	m.currentGroup = []document.Op{{}}

	msg := bufferResyncMsg{bufID: 7, content: "brand new server content\n", version: 42}
	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.buf.Content() != "brand new server content\n" {
		t.Errorf("buf.Content() = %q, want the resynced content", m2.buf.Content())
	}
	if m2.version != 42 {
		t.Errorf("version = %d, want 42", m2.version)
	}
	if len(m2.undoStack) != 0 || len(m2.redoStack) != 0 || m2.currentGroup != nil {
		t.Error("undo/redo/currentGroup should be cleared after a resync")
	}
	if !m2.buf.Dirty() {
		t.Error("resynced buffer should be marked dirty")
	}
	if m2.severeErr == "" {
		t.Error("expected a must-dismiss modal prompting the user to check their last change")
	}
	_ = cmd // reparseHighlight() is nil here since the test model has no highlighter configured
}

// TestBufferResyncMsgClampsCursor covers content shrinking under the
// cursor: the old position may no longer exist in the resynced content.
func TestBufferResyncMsgClampsCursor(t *testing.T) {
	m := newTestModel("a very long original line\n")
	m.bufID = 1
	m.cursor = document.Pos{Line: 0, Col: 20}

	msg := bufferResyncMsg{bufID: 1, content: "hi\n", version: 2}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.cursor.Line != 0 || m2.cursor.Col > 2 {
		t.Errorf("cursor = %+v, want clamped within the new (shorter) content", m2.cursor)
	}
}

// TestBufferResyncMsgAdoptsRenamedPath is a regression test: resyncFromServer
// used to call OpenFile keyed by the client's own remembered m.filePath,
// which goes stale if a *different* client renamed the buffer via SaveAs —
// OpenFile(oldPath) would then spuriously open a second buffer for the old
// (possibly now-nonexistent) path instead of finding the existing one.
// GetBufferSnapshot is keyed by bufferID instead, so the resync must adopt
// whatever path it reports.
func TestBufferResyncMsgAdoptsRenamedPath(t *testing.T) {
	m := newTestModel("old content\n")
	m.bufID = 1
	m.filePath = "/tmp/old.go"

	msg := bufferResyncMsg{bufID: 1, content: "new content\n", version: 3, path: "/tmp/new.go"}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.filePath != "/tmp/new.go" {
		t.Errorf("filePath = %q, want %q", m2.filePath, "/tmp/new.go")
	}
	if m2.buf.Path() != "/tmp/new.go" {
		t.Errorf("buf.Path() = %q, want %q", m2.buf.Path(), "/tmp/new.go")
	}
}

// TestBufferResyncMsgDiscardsStaleBuffer mirrors inlayHintsMsg/
// semanticTokensMsg's bufID staleness check.
func TestBufferResyncMsgDiscardsStaleBuffer(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1

	msg := bufferResyncMsg{bufID: 2, content: "should not apply\n", version: 9}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.buf.Content() != "hello\n" {
		t.Errorf("buf.Content() = %q, want unchanged (stale bufID)", m2.buf.Content())
	}
}

// TestBufferResyncMsgHandlesFetchError verifies a failed resync fetch just
// surfaces a status message without touching the buffer.
func TestBufferResyncMsgHandlesFetchError(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1

	msg := bufferResyncMsg{bufID: 1, err: errTest}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.buf.Content() != "hello\n" {
		t.Errorf("buf.Content() = %q, want unchanged after a failed resync fetch", m2.buf.Content())
	}
	if m2.severeErr == "" {
		t.Error("expected a must-dismiss error modal after a failed resync fetch")
	}
}
