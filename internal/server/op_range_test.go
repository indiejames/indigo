package server

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestApplyOpRejectsAPositionOutsideTheBuffer: Buffer.Apply clamps a position
// that does not exist to the nearest one that does, so an op computed against
// text the server does not hold — a window that has fallen out of step — used
// to land at the end of a line or the buffer instead, and say nothing. It is
// now refused, which the window answers by resyncing.
func TestApplyOpRejectsAPositionOutsideTheBuffer(t *testing.T) {
	_, entry, cl := newOTTestService(t, "one\ntwo\n", 1)
	for _, op := range []document.Op{
		{Type: document.OpInsert, InsertLine: 0, InsertCol: 9, InsertText: "X"},
		{Type: document.OpInsert, InsertLine: 7, InsertCol: 0, InsertText: "X"},
		{Type: document.OpDelete, FromLine: 1, FromCol: 0, ToLine: 4, ToCol: 0},
	} {
		if err := sendOp(t, cl, 1, entry.buf.Version(), op); err == nil {
			t.Errorf("op %+v was accepted; the buffer became %q", op, entry.buf.Content())
		}
	}
	if got := entry.buf.Content(); got != "one\ntwo\n" {
		t.Fatalf("buffer = %q, want it unchanged", got)
	}
	// And an op in range still applies.
	if err := sendOp(t, cl, 1, entry.buf.Version(), document.Op{
		Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!",
	}); err != nil {
		t.Fatal(err)
	}
	if got := entry.buf.Content(); got != "one!\ntwo\n" {
		t.Fatalf("buffer = %q", got)
	}
}
