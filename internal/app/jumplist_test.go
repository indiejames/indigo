package app

import (
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// --- helpers ---

func newJumpApp() *App {
	return &App{jumpIdx: -1}
}

// rec records an edit with no line-count change.
func rec(a *App, file string, line, depth int) {
	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  file,
		Line:      line,
		UndoDepth: depth,
	})
}

// recDelta records an edit that also shifts existing entries.
// atLine is the first affected line; delta is the net line-count change.
func recDelta(a *App, file string, line, depth, atLine, delta int) {
	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  file,
		Line:      line,
		UndoDepth: depth,
		AtLine:    atLine,
		LineDelta: delta,
	})
}

// undoOp simulates an undo, providing the reverse line shift the undo caused.
func undoOp(a *App, file string, newDepth, atLine, delta int) {
	a.handleUndoJump(client.UndoMsg{
		FilePath:  file,
		NewDepth:  newDepth,
		AtLine:    atLine,
		LineDelta: delta,
	})
}

// back calls doJumpBack and returns the resulting App.
func back(a App) App {
	m, _ := a.doJumpBack()
	return m.(App)
}

// fwd calls doJumpForward and returns the resulting App.
func fwd(a App) App {
	m, _ := a.doJumpForward()
	return m.(App)
}

// activeLines returns the line numbers of all active jump entries, in list order.
func activeLines(a *App) []int {
	var out []int
	for _, e := range a.jumpList {
		if e.active {
			out = append(out, e.line)
		}
	}
	return out
}

// currentLine returns the line of the entry jumpIdx points to, or -1.
func currentLine(a App) int {
	if a.jumpIdx < 0 || a.jumpIdx >= len(a.jumpList) {
		return -1
	}
	return a.jumpList[a.jumpIdx].line
}

// --- basic recording ---

func TestJumpListBasicRecord(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 30, 2)

	lines := activeLines(a)
	if len(lines) != 2 || lines[0] != 5 || lines[1] != 30 {
		t.Errorf("want [5 30], got %v", lines)
	}
}

// --- line shift on forward delete ---

func TestJumpListDeleteShiftsEntry(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 30, 2)
	// Delete lines 10–19 (N=10): deletedTo=20. Entry at 30 → shifts to 20.
	recDelta(a, "f", 10, 3, 10, -10)

	var lineB int
	for _, e := range a.jumpList {
		if e.undoDepth == 2 {
			lineB = e.line
		}
	}
	if lineB != 20 {
		t.Errorf("entry B: want line 20 after delete, got %d", lineB)
	}
}

// --- entry in deleted range becomes inactive ---

func TestJumpListInactiveOnDelete(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 20, 1)
	// Delete lines 15–24 (N=10): entry at 20 is inside [15,25).
	recDelta(a, "f", 15, 2, 15, -10)

	// List must still contain the original entry (now inactive).
	found := false
	for _, e := range a.jumpList {
		if e.undoDepth == 1 {
			if e.active {
				t.Error("original entry should be inactive, not active")
			}
			if e.line != 20 {
				t.Errorf("inactive entry: want line 20, got %d", e.line)
			}
			if e.deactivatedDepth != 2 {
				t.Errorf("deactivatedDepth: want 2, got %d", e.deactivatedDepth)
			}
			found = true
		}
	}
	if !found {
		t.Error("original entry was dropped instead of marked inactive")
	}
}

// --- undo delete reactivates the suspended entry ---

func TestJumpListReactivateOnUndo(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 20, 1)
	recDelta(a, "f", 15, 2, 15, -10) // suspends entry at 20
	undoOp(a, "f", 1, 15, +10)       // undo delete

	lines := activeLines(a)
	if len(lines) != 1 || lines[0] != 20 {
		t.Errorf("after undo: want [20], got %v", lines)
	}
}

// --- undo reverses the line shift for surviving entries ---

func TestJumpListUndoRestoresShiftedEntry(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 30, 2)
	recDelta(a, "f", 10, 3, 10, -10) // delete lines 10-19; B shifts 30→20
	undoOp(a, "f", 2, 10, +10)       // undo delete

	lines := activeLines(a)
	found5, found30 := false, false
	for _, l := range lines {
		if l == 5 {
			found5 = true
		}
		if l == 30 {
			found30 = true
		}
	}
	if !found5 {
		t.Error("want entry at line 5")
	}
	if !found30 {
		t.Errorf("want entry at line 30 (restored from 20); active lines: %v", lines)
	}
}

// --- boundary: entry exactly at deletedTo shifts to atLine, undo restores it ---

func TestJumpListBoundaryEntryAtDeletedTo(t *testing.T) {
	a := newJumpApp()
	// Entry at 30. Delete lines 10–29 (N=20, atLine=10, deletedTo=30).
	// Entry at 30 is exactly at deletedTo → shifts to 10.
	rec(a, "f", 30, 1)
	recDelta(a, "f", 10, 2, 10, -20)

	// Find any entry that was originally at 30 (now shifted to 10).
	foundAt10 := false
	for _, e := range a.jumpList {
		if e.line == 10 && e.undoDepth == 1 {
			foundAt10 = true
		}
	}
	if !foundAt10 {
		t.Errorf("after delete: want entry shifted from 30 to 10 (undoDepth=1), list: %+v", a.jumpList)
	}

	undoOp(a, "f", 1, 10, +20) // undo delete

	lines := activeLines(a)
	found30 := false
	for _, l := range lines {
		if l == 30 {
			found30 = true
		}
	}
	if !found30 {
		t.Errorf("after undo: want entry at 30 restored; active lines: %v", lines)
	}
}

// --- dedup must not overwrite undoDepth of a shifted entry ---
// When an entry at line X is shifted to line Y by a delete, and the delete
// cursor is also at Y, deduplication must not overwrite the shifted entry's
// undoDepth with the delete's undoDepth (which would cause it to be pruned
// when the delete is undone).

func TestJumpListShiftedEntryKeepsUndoDepth(t *testing.T) {
	a := newJumpApp()
	// Entry at 30 (depth 1). Delete 20 lines starting at 10: deletedTo=30.
	// Entry shifts from 30 to 10. Delete cursor is also at 10.
	rec(a, "f", 30, 1)
	recDelta(a, "f", 10, 2, 10, -20) // cursor=10, same as shifted entry

	// After undo the delete, the entry at 30 must survive.
	undoOp(a, "f", 1, 10, +20)

	lines := activeLines(a)
	found := false
	for _, l := range lines {
		if l == 30 {
			found = true
		}
	}
	if !found {
		t.Errorf("original entry at 30 lost after shift+dedup+undo; active lines: %v", lines)
	}
}

// --- navigation skips inactive entries ---

func TestJumpListNavigationSkipsInactive(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 20, 2) // will be suspended
	rec(a, "f", 40, 3)
	// Delete lines 15–29 (N=15): entry at 20 in [15,30) → inactive.
	// Entry at 40: 40 >= 30 → shifts to 25.
	recDelta(a, "f", 15, 4, 15, -15)

	// Navigate back through all active entries; should visit 4→25→5, skipping 20.
	a1 := back(*a)
	a2 := back(a1)
	a3 := back(a2)

	got := []int{currentLine(a1), currentLine(a2), currentLine(a3)}
	// Expected: last active entry (depth 4, line 15), then depth 3 (line 25), then depth 1 (line 5).
	if got[len(got)-1] != 5 {
		t.Errorf("last jump-back should reach line 5; got sequence %v", got)
	}
	for _, l := range got {
		if l == 20 {
			t.Errorf("navigation visited inactive entry at line 20; sequence: %v", got)
		}
	}
}

// --- the user's exact scenario ---
// Edit at A, edit at B (25 lines later), delete lines between,
// undo the delete, undo the insert at B, then navigate → should reach A.

func TestJumpListUserScenario(t *testing.T) {
	a := newJumpApp()
	const (
		A = 10
		B = 35
	)
	rec(a, "f", A, 1) // insert at A
	rec(a, "f", B, 2) // insert at B
	// Delete lines 15–24 (N=10, atLine=15, deletedTo=25).
	// B=35 >= 25 → shifts to 25.
	recDelta(a, "f", 15, 3, 15, -10)

	undoOp(a, "f", 2, 15, +10) // undo delete: B restored to 35, delete entry pruned
	undoOp(a, "f", 1, B, 0)    // undo insert B (no newlines → lineDelta=0)

	lines := activeLines(a)
	if len(lines) != 1 || lines[0] != A {
		t.Errorf("after two undos: want [%d], got %v", A, lines)
	}

	a2 := back(*a)
	if currentLine(a2) != A {
		t.Errorf("jump-back after two undos: want line %d, got %d (jumpIdx=%d)",
			A, currentLine(a2), a2.jumpIdx)
	}
}

// --- user scenario with navigation before undos ---
// Same edits, but user navigates (presses -) during the session before undoing.
// This exercises the jumpIdx-reset-on-undo path.

func TestJumpListUserScenarioWithNavBeforeUndo(t *testing.T) {
	a := newJumpApp()
	const (
		A = 10
		B = 35
	)
	rec(a, "f", A, 1)
	rec(a, "f", B, 2)
	recDelta(a, "f", 15, 3, 15, -10) // delete; B shifts to 25

	// User presses - twice before undoing.
	a1 := back(*a) // → delete entry (depth 3)
	a2 := back(a1) // → B entry (depth 2, line 25)

	undoOp(&a2, "f", 2, 15, +10) // undo delete
	undoOp(&a2, "f", 1, B, 0)    // undo insert B

	if a2.jumpIdx != -1 {
		t.Errorf("jumpIdx should be -1 after undo, got %d", a2.jumpIdx)
	}

	a3 := back(a2)
	if currentLine(a3) != A {
		t.Errorf("jump-back after nav+two undos: want line %d, got %d",
			A, currentLine(a3))
	}
}

// --- jumpIdx resets to -1 after any undo ---

func TestJumpListJumpIdxResetOnUndo(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 30, 2)
	rec(a, "f", 50, 3)

	// Navigate back twice: jumpIdx points somewhere into the list.
	a1 := back(*a) // → 50
	a2 := back(a1) // → 30

	undoOp(&a2, "f", 2, 50, 0)
	if a2.jumpIdx != -1 {
		t.Errorf("after undo: want jumpIdx=-1, got %d", a2.jumpIdx)
	}

	// Next jump-back should start from end of surviving list.
	a3 := back(a2)
	if currentLine(a3) != 30 {
		t.Errorf("want jump to line 30 after undo+jump-back, got %d", currentLine(a3))
	}
}

// --- forward navigation after backward ---

func TestJumpListForwardNavigation(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 20, 2)
	rec(a, "f", 40, 3)

	a1 := back(*a) // → 40
	a2 := back(a1) // → 20
	a3 := back(a2) // → 5
	a4 := fwd(a3)  // → 20

	if currentLine(a4) != 20 {
		t.Errorf("after back×3, fwd×1: want line 20, got %d", currentLine(a4))
	}
}

// --- multiple undos empty the list ---

func TestJumpListAllUndosEmptyList(t *testing.T) {
	a := newJumpApp()
	rec(a, "f", 5, 1)
	rec(a, "f", 20, 2)

	undoOp(a, "f", 1, 20, 0)
	undoOp(a, "f", 0, 5, 0)

	lines := activeLines(a)
	if len(lines) != 0 {
		t.Errorf("after all undos: want empty list, got %v", lines)
	}

	// Navigating on an empty list should be a no-op, not panic.
	a2 := back(*a)
	if a2.jumpIdx != -1 {
		t.Errorf("jump-back on empty list: jumpIdx should be -1, got %d", a2.jumpIdx)
	}
}

// --- different files are isolated ---

func TestJumpListMultipleFiles(t *testing.T) {
	a := newJumpApp()
	rec(a, "a.go", 10, 1)
	rec(a, "b.go", 20, 2)
	// Delete in a.go should not affect b.go entry.
	recDelta(a, "a.go", 5, 3, 5, -8)

	var bLine int
	for _, e := range a.jumpList {
		if e.filePath == "b.go" {
			bLine = e.line
		}
	}
	if bLine != 20 {
		t.Errorf("b.go entry should not shift; want 20, got %d", bLine)
	}
}
