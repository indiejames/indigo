package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/proto"
)

// TestMoveTextToFileDeliversToTheInitiatingClient is a regression test for
// applyServerOriginated's exclusion argument being given the initiating client.
//
// That argument means "this client already holds the op locally", which is true
// of an op a client sent through ApplyOp and false of one the server computed
// itself. MoveTextToFile passes it clientID, so the one window that asked for
// the move was the one window never told about it: the text stayed on screen
// there, diverging from the server with nothing to signal it, until some
// unrelated event forced a resync.
func TestMoveTextToFileDeliversToTheInitiatingClient(t *testing.T) {
	s, entry, cl := newOTTestService(t, "keep\nMOVE ME\nkeep\n", 1, 2)
	dest := filepath.Join(t.TempDir(), "dest.go")

	fut, rel := cl.MoveTextToFile(context.Background(), func(p proto.EditorService_moveTextToFile_Params) error {
		p.SetClientId(1)
		p.SetBufId(1)
		p.SetFromLine(1)
		p.SetFromCol(0)
		p.SetToLine(2)
		p.SetToCol(0)
		return p.SetDestPath(dest)
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("MoveTextToFile: %v", err)
	}

	if got := entry.buf.Content(); got != "keep\nkeep\n" {
		t.Fatalf("buffer = %q, want the moved text removed", got)
	}

	s.mu.Lock()
	forInitiator := len(entry.outgoing[1])
	forOther := len(entry.outgoing[2])
	s.mu.Unlock()

	if forInitiator == 0 {
		t.Error("the initiating client was queued no ops — it asked the server to " +
			"make this edit and never applied it locally, so its window still shows " +
			"the moved text")
	}
	if forOther == 0 {
		t.Error("the other client was queued no ops")
	}
}

// TestAppendTextToFileDeliversToTheInitiatingClient is the same check for the
// destination half, which has the same shape and the same mistake: the client
// that asked for the move may have the destination file open in another tab.
func TestAppendTextToFileDeliversToTheInitiatingClient(t *testing.T) {
	s, entry, _ := newOTTestService(t, "existing\n", 1, 2)

	if err := s.appendTextToFile(1, entry.buf.Path(), "APPENDED"); err != nil {
		t.Fatalf("appendTextToFile: %v", err)
	}

	s.mu.Lock()
	forInitiator := len(entry.outgoing[1])
	s.mu.Unlock()

	if forInitiator == 0 {
		t.Error("the initiating client was queued no ops for the destination buffer")
	}
}
