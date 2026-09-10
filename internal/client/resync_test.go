package client

import (
	"errors"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
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

	// failureCause set: this is the post-ApplyOp-failure resync, where the modal is
	// warranted. The routine generation-change resync is covered separately by
	// TestBufferResyncAfterServerChangeIsNotAnError.
	msg := bufferResyncMsg{bufID: 7, content: "brand new server content\n", version: 42, failureCause: "rpc: connection closed"}
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

// TestBufferResyncMsgAdoptsRenamedPathDetectsShebang verifies a resync that
// renames the buffer to an extension-less path (e.g. another client's
// SaveAs) also runs the shebang fallback, exactly as New() does for a
// freshly opened buffer — a rename shouldn't behave differently from an
// initial open just because it goes through a different code path.
func TestBufferResyncMsgAdoptsRenamedPathDetectsShebang(t *testing.T) {
	if highlight.NewForKey("sh") == nil {
		t.Skip("no bash highlighter registered; run with -tags lang_all (or lang_bash)")
	}
	m := newTestModel("old content\n")
	m.bufID = 1
	m.filePath = "/tmp/old.go"

	msg := bufferResyncMsg{
		bufID:   1,
		content: "#!/usr/bin/env bash\necho hi\n",
		version: 3,
		path:    "/tmp/myscript",
	}
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.langOverride != "sh" {
		t.Errorf("langOverride = %q, want %q (detected from shebang)", m2.langOverride, "sh")
	}
	if m2.hlr == nil {
		t.Error("hlr = nil, want the bash highlighter detected from the shebang")
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

// TestDescribeOpSummarisesWithoutLeakingContent covers the log line's payload.
// It has to identify the op well enough to correlate with a buffer state, while
// not writing the user's text into a file in the temp directory — an insert can
// be an entire paste.
func TestDescribeOpSummarisesWithoutLeakingContent(t *testing.T) {
	ins := describeOp(document.Op{
		Type: document.OpInsert, InsertLine: 4, InsertCol: 2, InsertText: "hunter2-secret",
	})
	if !strings.Contains(ins, "4:2") {
		t.Errorf("insert description %q omits the position", ins)
	}
	if strings.Contains(ins, "hunter2") {
		t.Errorf("insert description %q contains the inserted text; a log in the temp "+
			"directory must not hold the user's content", ins)
	}
	if !strings.Contains(ins, "14 bytes") {
		t.Errorf("insert description %q omits the size, which is what makes it correlatable", ins)
	}

	del := describeOp(document.Op{Type: document.OpDelete, FromLine: 1, FromCol: 0, ToLine: 3, ToCol: 5})
	if !strings.Contains(del, "1:0-3:5") {
		t.Errorf("delete description %q omits the range", del)
	}
}

// The recovery fetch must get a longer budget than the edit that failed.
// Whatever made the edit time out is still true when the resync goes out a
// moment later, so an equal budget turns one slow moment into "buffer may be
// out of sync with the server" — a message that reads as data loss and is not.
func TestResyncBudgetExceedsEditBudget(t *testing.T) {
	if resyncTimeout <= applyOpTimeout {
		t.Errorf("resyncTimeout (%v) must exceed applyOpTimeout (%v), or a transient stall "+
			"escalates straight to an unrecoverable-sounding error", resyncTimeout, applyOpTimeout)
	}
}

// TestBufferResyncAfterServerChangeIsNotAnError is a regression test for a red
// "Error" modal appearing during entirely normal work.
//
// Two different events start a resync, and only one of them is a problem. An
// ApplyOp failure means an edit never reached the server — worth interrupting
// for. A generation change means the server replaced the buffer *because it was
// asked to*: format-on-save, SaveAs, or an explicit Format. Working alongside an
// agent makes the second routine — every save_file on a file with
// format_on_save fires one — and reporting it as "Error ... please check your
// last change" says something went wrong when nothing did, which is exactly how
// a user learns to dismiss modals without reading them.
func TestBufferResyncAfterServerChangeIsNotAnError(t *testing.T) {
	m := newTestModel("original\n")
	m.bufID = 7
	m.undoStack = []undoEntry{{}}

	msg := bufferResyncMsg{bufID: 7, content: "reformatted\n", version: 42} // no failureCause
	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.severeErr != "" {
		t.Errorf("severeErr = %q; a server-side reformat is routine and must not raise a "+
			"must-dismiss error modal", m2.severeErr)
	}
	// The content still has to be adopted, and the undo history still discarded:
	// its inverse ops describe pre-resync content either way.
	if m2.buf.Content() != "reformatted\n" {
		t.Errorf("buf.Content() = %q, want the resynced content", m2.buf.Content())
	}
	if len(m2.undoStack) != 0 {
		t.Error("undo stack should be cleared after any resync, benign or not")
	}
}

// TestResyncModalNamesTheCause is a regression test for a failure that could
// not be diagnosed from the screen.
//
// severeErr is a single field, so the modal raised by applyOpFailedMsg — the
// one carrying the server's actual error — is overwritten by the resync's own
// modal milliseconds later. The user is left looking at "Buffer resynced from
// server" with no indication of what failed, which is exactly how it was
// reported. The surviving message must name the cause.
func TestResyncModalNamesTheCause(t *testing.T) {
	m := newTestModel("original\n")
	m.bufID = 7

	// The failure arrives first and raises its own modal.
	updated, _ := m.Update(applyOpFailedMsg{bufID: 7, err: errors.New("rpc: connection closed")})
	m1 := updated.(Model)
	if m1.severeErr == "" {
		t.Fatal("no modal on the failure itself; a resync that never completes would be silent")
	}

	// Then the resync succeeds and replaces it. What is left on screen has to
	// still say why.
	updated2, _ := m1.Update(bufferResyncMsg{
		bufID: 7, content: "server content\n", version: 9, failureCause: "rpc: connection closed",
	})
	m2 := updated2.(Model)
	if m2.severeErr == "" {
		t.Fatal("expected the resync modal")
	}
	if !strings.Contains(m2.severeErr, "rpc: connection closed") {
		t.Errorf("surviving modal = %q; it must name the cause, or the failure is "+
			"undiagnosable from the screen", m2.severeErr)
	}
}
