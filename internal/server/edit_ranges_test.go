package server

import (
	"slices"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/plugin"
)

// TestEditRangesCarryColumns pins what plugins are told about each edit: the
// range it replaced, with columns, so a join (a delete from the end of a line)
// and a line opened above (an insert at column 0) are distinguishable from a
// deleted line and a line opened below.
func TestEditRangesCarryColumns(t *testing.T) {
	ops := []document.Op{
		{Type: document.OpInsert, InsertLine: 4, InsertCol: 0, InsertText: "new\n"},      // O on line 4
		{Type: document.OpDelete, FromLine: 2, FromCol: 9, ToLine: 3, ToCol: 0},          // J on line 2
		{Type: document.OpInsert, InsertLine: 1, InsertCol: 2, InsertText: "no newline"}, // same line count
		{Type: document.OpDelete, FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 4},          // same line count
		{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "a\nb\nc"},    // two lines
		{Type: document.OpDelete, FromLine: 5, FromCol: 0, ToLine: 8, ToCol: 2},          // three lines
	}
	want := []plugin.EditRange{
		{FromLine: 4, FromCol: 0, ToLine: 4, ToCol: 0, LineDelta: 1},
		{FromLine: 2, FromCol: 9, ToLine: 3, ToCol: 0, LineDelta: -1},
		{FromLine: 0, FromCol: 3, ToLine: 0, ToCol: 3, LineDelta: 2},
		{FromLine: 5, FromCol: 0, ToLine: 8, ToCol: 2, LineDelta: -3},
	}
	if got := editRanges(ops); !slices.Equal(got, want) {
		t.Fatalf("editRanges = %+v\nwant %+v", got, want)
	}
}
