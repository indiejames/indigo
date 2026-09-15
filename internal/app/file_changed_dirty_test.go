package app

import (
	"testing"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// TestFileChangedDoesNotReloadOverLocalEdits is a regression test for the
// silent-reload branch trusting the server's dirty snapshot alone.
//
// handleExternalWrite reads entry.buf.Dirty() when it dispatches, then fans out
// to clients concurrently with a per-client timeout. Two external writes can
// therefore be reported out of order, and an older dirty=false can arrive after
// the user has started typing. This branch reloads with no prompt, so acting on
// it discards their unsaved work with nothing shown and nothing to undo.
//
// The client's own buffer is the authority on its unsaved state, and reading it
// here — at handling time — is what no ordering of server messages can
// invalidate.
func TestFileChangedDoesNotReloadOverLocalEdits(t *testing.T) {
	m := newReloadTestModel(1, "/tmp/a.go")
	// A local edit that has not been saved — the user typing while the
	// notification was in flight.
	m, _ = m.ApplyExternalOps([]document.Op{{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "typed",
	}})

	if !m.Dirty() {
		t.Fatal("test setup: the buffer should be dirty after an edit")
	}

	a := App{
		buffers:        []client.Model{m},
		active:         0,
		width:          80,
		height:         24,
		cfg:            &config.Config{},
		fileChangedIdx: -1,
	}

	// A stale notification: the server believed the buffer was clean when it
	// sampled, before this client's edit reached it.
	updated, cmd := a.Update(client.FileChangedMsg{BufID: 1, Dirty: false})
	a2 := updated.(App)

	if cmd != nil {
		t.Error("a reload was started over a locally-dirty buffer — the user's " +
			"unsaved edits are discarded with no prompt")
	}
	if a2.fileChangedIdx != 0 {
		t.Errorf("fileChangedIdx = %d, want 0: the user must be asked, not overruled",
			a2.fileChangedIdx)
	}
}

// TestFileChangedReloadsSilentlyWhenTrulyClean keeps the fix honest: when
// nothing is unsaved on either side, the reload must still happen with no
// prompt. That is the whole point of the branch.
func TestFileChangedReloadsSilentlyWhenTrulyClean(t *testing.T) {
	a := App{
		buffers:        []client.Model{newReloadTestModel(1, "/tmp/a.go")},
		active:         0,
		width:          80,
		height:         24,
		cfg:            &config.Config{},
		fileChangedIdx: -1,
	}

	updated, cmd := a.Update(client.FileChangedMsg{BufID: 1, Dirty: false})
	a2 := updated.(App)

	if cmd == nil {
		t.Error("a clean buffer must reload silently")
	}
	if a2.fileChangedIdx != -1 {
		t.Errorf("fileChangedIdx = %d, want -1: no prompt for a clean buffer", a2.fileChangedIdx)
	}
}
