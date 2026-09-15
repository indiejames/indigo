package app

import (
	"testing"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestRemoteEditShiftsJumpEntries: another client inserting lines above a jump
// entry moves the line that entry points at, so the entry must move with it.
// Local edits do this through EditRecordMsg; remote ones emitted nothing, so
// the list silently went stale and a jump landed in the wrong place.
func TestRemoteEditShiftsJumpEntries(t *testing.T) {
	// jumpIdx starts at -1 in the real constructor; recordEdit truncates forward
	// history relative to it, so the zero value would slice an empty list.
	a := App{cfg: &config.Config{}, width: 80, height: 24, jumpIdx: -1}
	a.recordEdit("/tmp/a.go", 10, 0, 1)
	a.recordEdit("/tmp/a.go", 20, 0, 2)
	a.recordEdit("/tmp/other.go", 30, 0, 3)

	updated, _ := a.Update(client.RemoteEditMsg{
		FilePath: "/tmp/a.go", AtLine: 5, LineDelta: 3, UndoDepth: 4,
	})
	a2 := updated.(App)

	if a2.jumpList[0].line != 13 || a2.jumpList[1].line != 23 {
		t.Errorf("entries at lines %d and %d, want 13 and 23 after three lines were inserted above them",
			a2.jumpList[0].line, a2.jumpList[1].line)
	}
	if a2.jumpList[2].line != 30 {
		t.Errorf("entry in another file moved to %d; it should be untouched", a2.jumpList[2].line)
	}
}

// TestRemoteEditDoesNotRecordAJumpDestination is the half that distinguishes
// this from EditRecordMsg. The user did not edit where the other client did, so
// it is not somewhere to jump back to — and recording one would also truncate
// their forward history, which is still theirs.
func TestRemoteEditDoesNotRecordAJumpDestination(t *testing.T) {
	// jumpIdx starts at -1 in the real constructor; recordEdit truncates forward
	// history relative to it, so the zero value would slice an empty list.
	a := App{cfg: &config.Config{}, width: 80, height: 24, jumpIdx: -1}
	a.recordEdit("/tmp/a.go", 10, 0, 1)
	before := len(a.jumpList)

	updated, _ := a.Update(client.RemoteEditMsg{
		FilePath: "/tmp/a.go", AtLine: 5, LineDelta: 2, UndoDepth: 2,
	})
	a2 := updated.(App)

	if len(a2.jumpList) != before {
		t.Errorf("jump list grew from %d to %d; a remote edit must not add a destination",
			before, len(a2.jumpList))
	}
}
