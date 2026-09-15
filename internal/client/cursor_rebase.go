package client

import "github.com/indiejames/indigo/internal/document"

// shiftPositionsPastRemoteOp moves the *live* caret state through a remote op
// that is about to be applied.
//
// Buffer content is rebased by the transform, but a cursor, a selection and the
// extra cursors are positions too — they name a spot in the text, and an edit
// landing before them moves the text they name. Left alone they come to point at
// something else entirely, which reads as the caret drifting on its own and the
// next keystroke landing in the wrong place.
//
// Stored snapshots are deliberately *not* shifted, for the same reason stored
// undo ops are not: the stack is LIFO, so by the time an entry's caret snapshot
// is restored, every entry above it — including the one recorded for this remote
// op — has been undone and the document is back to the state that snapshot
// described. Shifting them too would count the remote op twice and leave the
// caret an edit's worth off after every undo.
func (m Model) shiftPositionsPastRemoteOp(op document.Op) Model {
	m.cursor = document.ShiftPos(m.cursor, op)
	m.sel = shiftSelection(m.sel, op)
	m.extraCursors = shiftExtras(m.extraCursors, op)
	return m
}

// shiftExtras moves each extra cursor and its selection.
func shiftExtras(cursors []ExtraCursor, op document.Op) []ExtraCursor {
	if len(cursors) == 0 {
		return cursors
	}
	out := make([]ExtraCursor, len(cursors))
	for i, c := range cursors {
		c.pos = document.ShiftPos(c.pos, op)
		c.sel = shiftSelection(c.sel, op)
		out[i] = c
	}
	return out
}

// shiftSelection moves both ends of a selection, returning a copy so the
// original — which other Model copies may still reference — is untouched.
func shiftSelection(sel *Selection, op document.Op) *Selection {
	if sel == nil {
		return nil
	}
	s := *sel
	s.Anchor = document.ShiftPos(s.Anchor, op)
	s.Head = document.ShiftPos(s.Head, op)
	return &s
}
