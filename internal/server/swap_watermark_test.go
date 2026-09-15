package server

import (
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestResetOutgoingClearsEveryPerClientWatermark guards the rule that a
// wholesale swap clears *all* of them. Each is counted in the replaced buffer's
// version space, and the new buffer restarts at 0, so a survivor is wrong in one
// of two ways: prunedThrough/appliedFromClient too high refuses later ops, and
// sinceByClient too high stops holding history back and lets ops be reclaimed
// that a client which has not polled since the swap still needs.
func TestResetOutgoingClearsEveryPerClientWatermark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	entry := &bufferEntry{
		buf:               document.New(path, "x\n"),
		clients:           map[uint64]struct{}{1: {}},
		outgoing:          map[uint64][]document.Op{1: {{Type: document.OpInsert}}},
		prunedThrough:     map[uint64]uint64{1: 42},
		appliedFromClient: map[uint64]uint64{1: 7},
		sinceByClient:     map[uint64]uint64{1: 42},
	}

	resetOutgoing(entry)

	if len(entry.outgoing) != 0 {
		t.Errorf("outgoing survived the swap: %+v", entry.outgoing)
	}
	if len(entry.prunedThrough) != 0 {
		t.Errorf("prunedThrough survived the swap: %+v", entry.prunedThrough)
	}
	if len(entry.appliedFromClient) != 0 {
		t.Errorf("appliedFromClient survived the swap: %+v", entry.appliedFromClient)
	}
	if len(entry.sinceByClient) != 0 {
		t.Errorf("sinceByClient survived the swap: %+v — it is counted in the replaced "+
			"buffer's version space and no longer holds TrimHistory back", entry.sinceByClient)
	}
}
