package client

import "github.com/indiejames/indigo/internal/document"

// Undo history has to be rebased when a remote op lands, for the same reason
// buffers do: the stored ops are coordinates into a document that just changed
// under them.
//
// This was broken long before operational transform arrived — any remote edit
// invalidated every entry below it, and the updatesMsg handler cheerfully
// pushed the remote op's own inverse on top of a stack it had just invalidated.
// The fix only became available once document.Transform existed.

// reverseOps returns ops in the opposite order. Entries store their ops
// forward but apply them backwards (see executeUndo), so rebasing has to work
// on the application order and store the result back the way it came.
func reverseOps(ops []document.Op) []document.Op {
	out := make([]document.Op, len(ops))
	for i, op := range ops {
		out[len(ops)-1-i] = op
	}
	return out
}

// rebaseEntryOps rebases one entry's ops past the carried op sequence, and
// returns the carried sequence rebased past the entry.
//
// Carrying matters: an entry deeper in the stack is expressed against the
// document as it will be once every entry above it has been applied, so it must
// be rebased past the remote op *as that op looks from there*, not as it looks
// now.
func rebaseEntryOps(stored, carried []document.Op) (newStored, newCarried []document.Op) {
	// The remote op wins ties — the server ordered it, and both ends must agree.
	rebased, forwarded := document.TransformSeq(reverseOps(stored), carried, false)
	return reverseOps(rebased), forwarded
}

// rebaseUndoHistory rebases stored edit history past a remote op that is about
// to be applied locally.
//
// It deliberately leaves m.undoStack alone, which is the opposite of what it
// looks like it should do. The updatesMsg handler makes remote ops undoable: it
// records their inverses and pushes them as the *top* undo entry. Undoing
// therefore walks back through the remote op before ever reaching the entries
// beneath it, and by the time those are applied the remote op has been removed
// again — so they are already expressed against the right document. Rebasing
// them as well counts the remote op twice, and an undo lands one edit's worth
// off. That is not hypothetical: it is what the first version of this function
// did, and TestUndoAfterRemoteOpAppliesAtTheRightPlace caught it.
//
// What does need rebasing is history that will end up *above* the remote op's
// entry, and so will be applied to a document that still contains it:
//
//   - currentGroup, the in-progress insert session, which is pushed as an entry
//     when the session ends and therefore lands on top;
//   - redoStack, whose entries replay onto the current document. The handler
//     clears redo on any incoming edit today, so this is belt and braces — but
//     it is the correct behaviour if that ever changes, and cheap.
//
// Both are traversed top-down, each entry rebased past the remote op and the
// remote op carried on past that entry, since a deeper entry is expressed
// against the document as it will be once the ones above it have been applied.
func (m Model) rebaseUndoHistory(remote document.Op) Model {
	// currentGroup is deliberately not rebased. The updatesMsg handler closes
	// any open session into its own entry *before* calling this, precisely so
	// the stack's order matches the order edits were applied — which puts those
	// ops below this remote op, where LIFO makes them valid again once it is
	// undone. Rebasing them as well would count the remote op twice, the same
	// mistake as rebasing the undo stack.

	// Copied before being written to. Model is a value type, so the slice header
	// is copied on every Update but the backing array is not — mutating entries
	// in place would reach every other copy of the Model, including ones
	// captured by in-flight commands.
	if len(m.redoStack) > 0 {
		carried := []document.Op{remote}
		stack := append([]undoEntry(nil), m.redoStack...)
		for i := len(stack) - 1; i >= 0; i-- {
			stack[i].ops, carried = rebaseEntryOps(stack[i].ops, carried)
		}
		m.redoStack = stack
	}
	return m
}
