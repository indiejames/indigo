package client

import (
	"testing"
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
