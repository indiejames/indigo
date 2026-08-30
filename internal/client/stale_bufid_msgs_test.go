package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestSavedMsgIgnoredOnStaleBufID is a regression test: savedMsg previously
// carried no bufID at all, so a slow Save response landing after the user
// switched tabs would clear the dirty flag on whatever buffer happened to be
// active, not the one that was actually saved.
func TestSavedMsgIgnoredOnStaleBufID(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.buf.MarkDirty()

	res, _ := m.Update(savedMsg{bufID: 2})
	got := res.(Model)
	if !got.buf.Dirty() {
		t.Error("buffer marked clean despite savedMsg's bufID not matching the active buffer")
	}

	res2, _ := m.Update(savedMsg{bufID: 1})
	got2 := res2.(Model)
	if got2.buf.Dirty() {
		t.Error("buffer should be marked clean when savedMsg's bufID matches")
	}
}

// TestSavedAsMsgIgnoredOnStaleBufID is a regression test for a genuine
// cross-buffer data-loss bug: savedAsMsg previously carried no bufID, so a
// slow SaveAs response for buffer A, landing after the user switched to
// buffer B, would repoint B's filePath/identity at A's new path and mark B
// clean — hiding B's real unsaved changes behind the wrong filename.
func TestSavedAsMsgIgnoredOnStaleBufID(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 2
	m.filePath = "/tmp/b.go"
	m.buf.MarkDirty()

	// savedAsMsg for a *different* buffer (bufID 1) must not touch buffer 2.
	res, _ := m.Update(savedAsMsg{bufID: 1, newPath: "/tmp/a-renamed.go"})
	got := res.(Model)
	if got.filePath != "/tmp/b.go" {
		t.Errorf("filePath = %q, want unchanged %q (stale bufID)", got.filePath, "/tmp/b.go")
	}
	if !got.buf.Dirty() {
		t.Error("buffer should still be dirty — a stale savedAsMsg for another buffer must not mark it clean")
	}

	// Matching bufID applies normally.
	res2, _ := m.Update(savedAsMsg{bufID: 2, newPath: "/tmp/b-renamed.go"})
	got2 := res2.(Model)
	if got2.filePath != "/tmp/b-renamed.go" {
		t.Errorf("filePath = %q, want %q", got2.filePath, "/tmp/b-renamed.go")
	}
	if got2.buf.Dirty() {
		t.Error("buffer should be marked clean after a matching savedAsMsg")
	}
}

// TestDiscardRecoveryMsgIgnoredOnStaleBufID is a regression test: a slow
// DiscardRecovery response landing after a buffer switch must not replace
// whatever buffer is now active with the original (discarded) buffer's
// recovered content.
func TestDiscardRecoveryMsgIgnoredOnStaleBufID(t *testing.T) {
	m := newTestModel("current unsaved work\n")
	m.bufID = 1

	res, _ := m.Update(discardRecoveryMsg{bufID: 2, content: "stale original content\n"})
	got := res.(Model)
	if got.buf.Content() != "current unsaved work\n" {
		t.Errorf("buf.Content() = %q, want unchanged (stale bufID)", got.buf.Content())
	}

	res2, _ := m.Update(discardRecoveryMsg{bufID: 1, content: "recovered content\n"})
	got2 := res2.(Model)
	if got2.buf.Content() != "recovered content\n" {
		t.Errorf("buf.Content() = %q, want %q", got2.buf.Content(), "recovered content\n")
	}
}

// TestApplyOpFailedMsgIgnoredOnStaleBufID is a regression test: applyOpFailedMsg
// previously carried no bufID, so a slow ApplyOp failure for a buffer the user
// switched away from would trigger resyncFromServer() against whatever buffer
// is now active — which never diverged — while the buffer that actually failed
// was left silently corrupted with no resync ever triggered.
func TestApplyOpFailedMsgIgnoredOnStaleBufID(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.rpc = &RPC{}

	updated, cmd := m.Update(applyOpFailedMsg{bufID: 2, err: errTest})
	got := updated.(Model)
	if got.severeErr != "" {
		t.Errorf("severeErr = %q, want empty (stale bufID must not surface this buffer's error modal)", got.severeErr)
	}
	if cmd != nil {
		t.Error("expected no resync command for a stale-bufID applyOpFailedMsg")
	}

	updated2, cmd2 := m.Update(applyOpFailedMsg{bufID: 1, err: errTest})
	got2 := updated2.(Model)
	if got2.severeErr == "" {
		t.Error("expected an error modal when applyOpFailedMsg's bufID matches")
	}
	if cmd2 == nil {
		t.Error("expected a resync command when applyOpFailedMsg's bufID matches")
	}
}

// TestSavedMsgIgnoredOnVersionMismatch is a regression test: bufID matching
// isn't enough — savedMsg must also carry the buffer's version at the time
// Save was requested, or a local edit (or a remote op from updatesMsg)
// landing on the *same* buffer while the save RPC was in flight gets
// silently marked clean even though it was never written to disk. This
// mirrors the compare-and-swap check the server's own Save handler already
// does (server_buffer.go) — without it the client disagrees with the server
// about whether the buffer is actually dirty.
func TestSavedMsgIgnoredOnVersionMismatch(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.buf.MarkDirty()
	startVersion := m.buf.Version()

	// A local edit lands after the save started (simulated directly via
	// Apply, matching what applyOp/updatesMsg would do).
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})

	res, _ := m.Update(savedMsg{bufID: 1, version: startVersion})
	got := res.(Model)
	if !got.buf.Dirty() {
		t.Error("buffer marked clean despite a version mismatch — the disk write predates a later edit")
	}

	// No further edits: version matches, should mark clean normally.
	res2, _ := m.Update(savedMsg{bufID: 1, version: m.buf.Version()})
	got2 := res2.(Model)
	if got2.buf.Dirty() {
		t.Error("buffer should be marked clean when savedMsg's version matches")
	}
}

// TestSavedAsMsgIgnoredOnVersionMismatch mirrors
// TestSavedMsgIgnoredOnVersionMismatch for SaveAs: an edit landing on the
// same buffer while the SaveAs round trip is in flight must not silently
// repoint filePath/mark clean, since newPath's content on disk no longer
// matches the buffer.
func TestSavedAsMsgIgnoredOnVersionMismatch(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.filePath = "/tmp/old.go"
	m.buf.MarkDirty()
	startVersion := m.buf.Version()

	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})

	res, _ := m.Update(savedAsMsg{bufID: 1, version: startVersion, newPath: "/tmp/new.go"})
	got := res.(Model)
	if got.filePath != "/tmp/old.go" {
		t.Errorf("filePath = %q, want unchanged %q (version mismatch)", got.filePath, "/tmp/old.go")
	}
	if !got.buf.Dirty() {
		t.Error("buffer should still be dirty — a version-mismatched savedAsMsg must not mark it clean")
	}
}

// TestDiscardRecoveryMsgIgnoredOnVersionMismatch is a regression test:
// recoveryPrompt is dismissed the instant "n" is pressed, before the
// DiscardRecovery RPC (up to 5s) even starts, so the user can keep editing
// the buffer in that window. Applying msg.content on a version mismatch
// would silently discard those edits.
func TestDiscardRecoveryMsgIgnoredOnVersionMismatch(t *testing.T) {
	m := newTestModel("original\n")
	m.bufID = 1
	startVersion := m.buf.Version()

	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "edited "})

	res, _ := m.Update(discardRecoveryMsg{bufID: 1, version: startVersion, content: "discarded original\n"})
	got := res.(Model)
	if got.buf.Content() != "edited original\n" {
		t.Errorf("buf.Content() = %q, want the in-progress edit preserved (version mismatch)", got.buf.Content())
	}
}

// TestSaveFailedMsgRoutedByBufID verifies saveFailedMsg (the RoutableMsg
// replacement for a plain errorMsg on Save/SaveAs failure) is discarded for
// a non-matching bufID and shown for a matching one.
func TestSaveFailedMsgRoutedByBufID(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1

	res, _ := m.Update(saveFailedMsg{bufID: 2, err: errTest})
	if got := res.(Model); got.status != "" {
		t.Errorf("status = %q, want empty (stale bufID)", got.status)
	}

	res2, _ := m.Update(saveFailedMsg{bufID: 1, err: errTest})
	if got := res2.(Model); got.status == "" {
		t.Error("expected a status message when saveFailedMsg's bufID matches")
	}
}

// TestDiscardRecoveryFailedMsgRestoresPrompt is a regression test: a failed
// DiscardRecovery previously surfaced as a generic errorMsg with no way to
// retry, since recoveryPrompt is already dismissed by the time the failure
// is known. It must restore the prompt (on the right buffer) instead.
func TestDiscardRecoveryFailedMsgRestoresPrompt(t *testing.T) {
	m := newTestModel("hello\n")
	m.bufID = 1
	m.recoveryPrompt = false

	res, _ := m.Update(discardRecoveryFailedMsg{bufID: 2, err: errTest})
	if got := res.(Model); got.recoveryPrompt {
		t.Error("recoveryPrompt restored despite a stale bufID")
	}

	res2, _ := m.Update(discardRecoveryFailedMsg{bufID: 1, err: errTest})
	got2 := res2.(Model)
	if !got2.recoveryPrompt {
		t.Error("expected recoveryPrompt to be restored so the user can retry")
	}
	if got2.severeErr == "" {
		t.Error("expected an error modal reporting the failure")
	}
}
