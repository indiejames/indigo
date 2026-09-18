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
		Shifts:    []client.LineShift{{AtLine: atLine, Delta: delta}},
	})
}

// undoOp simulates an undo, providing the reverse line shift the undo caused.
func undoOp(a *App, file string, newDepth, atLine, delta int) {
	a.handleUndoJump(client.UndoMsg{
		FilePath: file,
		NewDepth: newDepth,
		Shifts:   []client.LineShift{{AtLine: atLine, Delta: delta}},
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

// TestApplyEditRecordAppliesEveryShift is the App half of the collapsed-shift
// fix. The client now sends the ordered shifts rather than one summed
// (AtLine, LineDelta), and each has to be applied at its own boundary.
//
// The entry under test sits *between* two edit points whose deltas cancel: a
// summed shift moves it by zero, when it should move down one.
func TestApplyEditRecordAppliesEveryShift(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 4, 1) // an entry at line 4

	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts: []client.LineShift{
			{AtLine: 1, Delta: 1},  // above it: pushes it down
			{AtLine: 7, Delta: -1}, // below it: must not touch it
		},
	})

	if got := a.jumpList[0].line; got != 5 {
		t.Errorf("entry line = %d, want 5 — the shift above it must apply even though "+
			"the two shifts sum to zero", got)
	}
}

// TestApplyEditRecordRecordsOneDestinationPerMessage guards the other half:
// however many shifts a message carries, it is one user action and adds one
// jump entry.
func TestApplyEditRecordRecordsOneDestinationPerMessage(t *testing.T) {
	a := newJumpApp()
	before := len(a.jumpList)

	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      9,
		UndoDepth: 1,
		Shifts: []client.LineShift{
			{AtLine: 1, Delta: 1},
			{AtLine: 7, Delta: -1},
			{AtLine: 9, Delta: 3},
		},
	})

	if got := len(a.jumpList) - before; got != 1 {
		t.Errorf("added %d jump entries, want 1 — three shifts are still one edit", got)
	}
}

// TestHandleUndoJumpAppliesEveryShift is the App half of the UndoMsg fix. The
// entry sits between two shifts whose deltas cancel, so a summed version moves
// it by nothing when it should move down one.
func TestHandleUndoJumpAppliesEveryShift(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 4, 1) // an entry at line 4

	a.handleUndoJump(client.UndoMsg{
		FilePath: "/tmp/a.go",
		NewDepth: 1, // keeps the entry: its undoDepth is not greater
		Shifts: []client.LineShift{
			{AtLine: 7, Delta: -1}, // below it: must not touch it
			{AtLine: 1, Delta: 1},  // above it: pushes it down
		},
	})

	if got := a.jumpList[0].line; got != 5 {
		t.Errorf("entry line = %d, want 5 — each shift must apply at its own "+
			"boundary even though the two sum to zero", got)
	}
}

// TestHandleUndoJumpReactivatedEntriesKeepTheirLine guards the subtlety the
// two-pass split introduced. Rule 2 restores an entry suspended by the edit
// being undone, and its stored position is already correct — so Rule 3 must
// skip it. That used to be a `continue` inside one loop; with the line
// arithmetic moved to a second pass it has to be carried across, and getting it
// wrong would shift exactly the entries that must not move.
func TestHandleUndoJumpReactivatedEntriesKeepTheirLine(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 10, 1)

	// A delete at depth 2 suspends that entry.
	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts:    []client.LineShift{{AtLine: 9, Delta: -3}},
	})
	var suspended *jumpEntry
	for i := range a.jumpList {
		if a.jumpList[i].line == 10 && !a.jumpList[i].active {
			suspended = &a.jumpList[i]
		}
	}
	if suspended == nil {
		t.Fatal("test setup: expected the entry at line 10 to be suspended")
	}

	// Undoing that delete restores the lines and reactivates the entry.
	a.handleUndoJump(client.UndoMsg{
		FilePath: "/tmp/a.go",
		NewDepth: 1,
		Shifts:   []client.LineShift{{AtLine: 9, Delta: 3}},
	})

	var found bool
	for _, e := range a.jumpList {
		if e.filePath == "/tmp/a.go" && e.active && e.line == 10 {
			found = true
		}
	}
	if !found {
		t.Errorf("jumpList = %+v, want the reactivated entry still at line 10 — its "+
			"stored position was already correct and must not be shifted again",
			a.jumpList)
	}
}

// redoOp simulates a redo and the forward line shift it re-applies.
func redoOp(a *App, file string, newDepth, atLine, delta int) {
	a.handleRedoJump(client.RedoMsg{
		FilePath: file,
		NewDepth: newDepth,
		Shifts:   []client.LineShift{{AtLine: atLine, Delta: delta}},
	})
}

// TestRedoRestoresJumpLinesAfterUndo is the round trip, and the property a user
// would actually notice: edit, undo, redo, and a jump entry is back where the
// edit put it. Before this, redo told the App nothing, so the entry kept the
// line the *undo* had moved it to and every later jump landed wrong.
func TestRedoRestoresJumpLinesAfterUndo(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 10, 1) // an entry at line 10

	// A forward edit inserting one line above it: the entry moves to 11.
	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts:    []client.LineShift{{AtLine: 2, Delta: 1}},
	})
	if got := lineOf(a, "/tmp/a.go", 1); got != 11 {
		t.Fatalf("test setup: entry line = %d, want 11 after the insert", got)
	}

	undoOp(a, "/tmp/a.go", 1, 2, -1)
	if got := lineOf(a, "/tmp/a.go", 1); got != 10 {
		t.Fatalf("after undo: entry line = %d, want 10", got)
	}

	redoOp(a, "/tmp/a.go", 2, 2, 1)
	if got := lineOf(a, "/tmp/a.go", 1); got != 11 {
		t.Errorf("after redo: entry line = %d, want 11 — the redo re-applies the "+
			"insert, so the entry must move back down with it", got)
	}
}

// lineOf returns the line of the entry with the given undoDepth, or -1.
func lineOf(a *App, file string, undoDepth int) int {
	for _, e := range a.jumpList {
		if e.filePath == file && e.undoDepth == undoDepth {
			return e.line
		}
	}
	return -1
}

// TestRedoReSuspendsEntriesADeleteHadRemoved checks the depth symmetry, which is
// the part most easily got wrong: NewDepth must be the depth *after* the redo,
// so a re-suspended entry carries the same deactivatedDepth the original delete
// gave it and a later undo reactivates it exactly as the first one did.
func TestRedoReSuspendsEntriesADeleteHadRemoved(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 10, 1)

	// A delete covering lines 9..12 at depth 2 suspends the entry at 10.
	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts:    []client.LineShift{{AtLine: 9, Delta: -3}},
	})
	if active := activeAt(a, "/tmp/a.go", 10); active {
		t.Fatal("test setup: the entry inside the deleted range should be suspended")
	}

	// Undo restores the lines and reactivates it.
	undoOp(a, "/tmp/a.go", 1, 9, 3)
	if !activeAt(a, "/tmp/a.go", 10) {
		t.Fatal("after undo: the entry should be active again at line 10")
	}

	// Redo re-applies the delete: it must be suspended again.
	redoOp(a, "/tmp/a.go", 2, 9, -3)
	if activeAt(a, "/tmp/a.go", 10) {
		t.Error("after redo: the entry is still active, but the delete that " +
			"suspended it has been re-applied")
	}

	// And undoing once more must reactivate it, which only works if the redo
	// recorded the same deactivatedDepth the original delete did.
	undoOp(a, "/tmp/a.go", 1, 9, 3)
	if !activeAt(a, "/tmp/a.go", 10) {
		t.Error("after undo following a redo: the entry should be active again — " +
			"the redo must re-suspend at the depth the original edit used")
	}
}

// activeAt reports whether an active entry sits at the given line.
func activeAt(a *App, file string, line int) bool {
	for _, e := range a.jumpList {
		if e.filePath == file && e.line == line && e.active {
			return true
		}
	}
	return false
}

// TestRedoMsgIsRoutedToTheHandler covers the wiring, not the arithmetic.
//
// The tests above call handleRedoJump directly, so they pass just as happily if
// Update never dispatches a RedoMsg to it — which is precisely the state this
// work started from, since executeRedo sent nothing and nothing received it.
// A jump list that silently stops being adjusted raises no error; it just puts
// a later jump in the wrong place.
func TestRedoMsgIsRoutedToTheHandler(t *testing.T) {
	a := App{jumpIdx: -1, buffers: []client.Model{newReloadTestModel(1, "/tmp/a.go")}}
	a.jumpList = []jumpEntry{{filePath: "/tmp/a.go", line: 10, undoDepth: 1, active: true}}

	updated, _ := a.Update(client.RedoMsg{
		FilePath: "/tmp/a.go",
		NewDepth: 2,
		Shifts:   []client.LineShift{{AtLine: 2, Delta: 1}},
	})
	a2 := updated.(App)

	if got := a2.jumpList[0].line; got != 11 {
		t.Errorf("entry line = %d, want 11 — Update must route RedoMsg to "+
			"handleRedoJump, not drop it into the active buffer", got)
	}
}

// TestUndoOfAGroupThatBothSuspendsAndShifts is the case a reactivated entry
// skipping *every* shift gets wrong.
//
// One applyBatch — an LSP code action, a search-and-replace commit — produces
// one undo entry holding several ops. If one of them is the delete that
// suspended a jump entry and another changes line counts elsewhere, then
// undoing the group must skip only the shift that restores the suspended
// entry's own lines. Skipping the rest leaves it short by their combined delta.
//
// Forward: insert a line at 2 moves the entry 10 -> 11, then a delete of 9..12
// suspends it, frozen at 11. Undoing restores [9,12) — which the entry's stored
// position already accounts for — and removes the inserted line, which it does
// not. Correct answer is 10.
func TestUndoOfAGroupThatBothSuspendsAndShifts(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 10, 1)

	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts: []client.LineShift{
			{AtLine: 2, Delta: 1},  // pushes the entry to 11
			{AtLine: 9, Delta: -3}, // covers 11: suspends it there
		},
	})
	if activeAt(a, "/tmp/a.go", 11) {
		t.Fatal("test setup: the entry should be suspended at line 11")
	}

	// Undo applies the group's inverses in reverse order.
	a.handleUndoJump(client.UndoMsg{
		FilePath: "/tmp/a.go",
		NewDepth: 1,
		Shifts: []client.LineShift{
			{AtLine: 9, Delta: 3},  // restores the range it sits in: skip this one
			{AtLine: 2, Delta: -1}, // removes the inserted line: must still apply
		},
	})

	if !activeAt(a, "/tmp/a.go", 10) {
		t.Errorf("jumpList = %+v, want the reactivated entry active at line 10 — a "+
			"reactivated entry skips only the shift that restores it, not every "+
			"shift in the group", a.jumpList)
	}
}

// TestReactivatedEntrySkipsOnlyOneRestoringShift guards the "consumed once"
// half: two restoring shifts over the same lines must not both be skipped.
func TestReactivatedEntrySkipsOnlyOneRestoringShift(t *testing.T) {
	a := newJumpApp()
	rec(a, "/tmp/a.go", 10, 1)

	a.applyEditRecord(client.EditRecordMsg{
		FilePath:  "/tmp/a.go",
		Line:      0,
		UndoDepth: 2,
		Shifts:    []client.LineShift{{AtLine: 9, Delta: -3}},
	})
	if activeAt(a, "/tmp/a.go", 10) {
		t.Fatal("test setup: the entry should be suspended at line 10")
	}

	a.handleUndoJump(client.UndoMsg{
		FilePath: "/tmp/a.go",
		NewDepth: 1,
		Shifts: []client.LineShift{
			{AtLine: 9, Delta: 3}, // the restoring shift: skipped
			{AtLine: 9, Delta: 2}, // a second insert there: must apply
		},
	})

	if !activeAt(a, "/tmp/a.go", 12) {
		t.Errorf("jumpList = %+v, want the entry at 12 (10, skip the restore, then +2)",
			a.jumpList)
	}
}
