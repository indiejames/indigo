package document

import "strings"

// Transform rebases op a onto a document that has already had op b applied,
// returning the ops to apply in a's place. This is the core of the operational
// transform that keeps concurrent editors convergent: a was computed against a
// document that did not contain b, so applying it unchanged would land at the
// wrong offsets.
//
// The result is a slice because the cases genuinely differ in arity. Usually one
// op; empty when a is entirely cancelled by b (it deleted only text b had
// already deleted); two when a delete has to split around an insert that landed
// inside its range.
//
// **Apply the returned ops in the order given.** For the splitting case that
// order is later-range-first, so the earlier range's coordinates stay valid
// rather than needing to be recomputed after its sibling is applied.
//
// aWins breaks the tie when both ops insert at the exact same position, where
// there is no content-based reason to prefer either order. It must be a stable
// property of the two ops — their client ids, say — and callers must pass
// complementary values to the two directions: Transform(a, b, true) alongside
// Transform(b, a, false). Deriving it from arrival order instead would make the
// two sides disagree, which is precisely the divergence this exists to prevent.
//
// Only TP1 is required of this function:
//
//	apply(apply(doc, a), Transform(b, a)) == apply(apply(doc, b), Transform(a, b))
//
// TP2 — the notoriously hard property — is not, because indigo has a central
// serializer: the server orders every op, so transforms are always pairwise
// along one client's path rather than across a lattice of peers.
func Transform(a, b Op, aWins bool) []Op {
	if isNoop(a) {
		return nil
	}
	if isNoop(b) {
		return []Op{a}
	}
	switch {
	case a.Type == OpInsert && b.Type == OpInsert:
		return transformInsertInsert(a, b, aWins)
	case a.Type == OpInsert && b.Type == OpDelete:
		return transformInsertDelete(a, b)
	case a.Type == OpDelete && b.Type == OpInsert:
		return transformDeleteInsert(a, b)
	case a.Type == OpDelete && b.Type == OpDelete:
		return transformDeleteDelete(a, b)
	}
	return []Op{a}
}

// isNoop reports whether op changes nothing, so nothing needs rebasing onto or
// out of it. An insert of empty text and a zero-width delete both qualify; both
// arise naturally (a delete fully cancelled by an earlier transform, say).
func isNoop(op Op) bool {
	switch op.Type {
	case OpInsert:
		return op.InsertText == ""
	case OpDelete:
		return op.FromLine == op.ToLine && op.FromCol == op.ToCol
	}
	return true
}

func insertPos(op Op) Pos  { return Pos{Line: op.InsertLine, Col: op.InsertCol} }
func deleteFrom(op Op) Pos { return Pos{Line: op.FromLine, Col: op.FromCol} }
func deleteTo(op Op) Pos   { return Pos{Line: op.ToLine, Col: op.ToCol} }

// posLess reports whether p is strictly before q.
func posLess(p, q Pos) bool {
	if p.Line != q.Line {
		return p.Line < q.Line
	}
	return p.Col < q.Col
}

// insertSpan measures inserted text the way positions care about: how many
// lines it adds, and how long its last line is. Columns are rune counts
// throughout this package, so lastLen is counted in runes, not bytes.
func insertSpan(text string) (lines, lastLen int) {
	lines = strings.Count(text, "\n")
	last := text
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		last = text[i+1:]
	}
	return lines, len([]rune(last))
}

// shiftForward maps a position through an insert applied before it.
//
// A position exactly at the insertion point does not move: the inserted text
// goes in ahead of it, which is what makes a delete starting exactly where text
// was inserted leave that text alone rather than swallowing it.
func shiftForward(p Pos, ins Op) Pos {
	at := insertPos(ins)
	if posLess(p, at) {
		return p
	}
	lines, lastLen := insertSpan(ins.InsertText)
	if p.Line > at.Line {
		return Pos{Line: p.Line + lines, Col: p.Col}
	}
	// Same line as the insertion, at or after it.
	if lines == 0 {
		return Pos{Line: p.Line, Col: p.Col + lastLen}
	}
	// The insert split this line: everything from the insertion point onward
	// moves down to the inserted text's last line, re-based at its end.
	return Pos{Line: p.Line + lines, Col: lastLen + (p.Col - at.Col)}
}

// shiftForwardExclusive is shiftForward with the boundary decided the other
// way: a position exactly at the insertion point stays put rather than moving
// past the inserted text.
//
// This is what a delete's *end* needs, and the asymmetry with its start is not
// arbitrary — both halves encode the same rule, that concurrently inserted text
// is never deleted. A delete's start sitting at the insertion point moves
// forward, leaving the new text before the range; its end sitting there must
// not move, leaving the new text after the range. Using the inclusive form for
// both silently extends the delete over text the other client just typed, which
// is a TP1 violation and a real loss of someone's work.
func shiftForwardExclusive(p Pos, ins Op) Pos {
	if p == insertPos(ins) {
		return p
	}
	return shiftForward(p, ins)
}

// shiftBackward maps a position through a delete applied before it. A position
// inside the deleted range collapses to its start, that text being gone.
func shiftBackward(p Pos, del Op) Pos {
	from, to := deleteFrom(del), deleteTo(del)
	if !posLess(from, p) {
		return p // at or before the deletion
	}
	if posLess(p, to) {
		return from // inside it
	}
	if p.Line > to.Line {
		return Pos{Line: p.Line - (to.Line - from.Line), Col: p.Col}
	}
	// On the deletion's last line: it joins onto the first line's remainder.
	return Pos{Line: from.Line, Col: from.Col + (p.Col - to.Col)}
}

func insertAtPos(op Op, p Pos) Op {
	op.InsertLine, op.InsertCol = p.Line, p.Col
	return op
}

func deleteRangeOp(op Op, from, to Pos) Op {
	op.FromLine, op.FromCol = from.Line, from.Col
	op.ToLine, op.ToCol = to.Line, to.Col
	return op
}

func transformInsertInsert(a, b Op, aWins bool) []Op {
	at, bt := insertPos(a), insertPos(b)
	if posLess(at, bt) {
		return []Op{a}
	}
	if at == bt && aWins {
		// a's text goes in first, so b's — already applied — sits after it and
		// a's position is untouched.
		return []Op{a}
	}
	return []Op{insertAtPos(a, shiftForward(at, b))}
}

func transformInsertDelete(a, b Op) []Op {
	return []Op{insertAtPos(a, shiftBackward(insertPos(a), b))}
}

func transformDeleteInsert(a, b Op) []Op {
	from, to := deleteFrom(a), deleteTo(a)
	at := insertPos(b)

	// The insert landed strictly inside the range being deleted. Widening the
	// delete to swallow it would destroy text the other client just typed, so
	// the delete splits around it instead and the inserted text survives.
	if posLess(from, at) && posLess(at, to) {
		later := deleteRangeOp(a, shiftForward(at, b), shiftForwardExclusive(to, b))
		earlier := deleteRangeOp(a, from, at)
		// Later range first: deleting it does not move anything at or before
		// `at`, so the earlier range's coordinates remain valid. The reverse
		// order would require recomputing them.
		return []Op{later, earlier}
	}
	return []Op{deleteRangeOp(a, shiftForward(from, b), shiftForwardExclusive(to, b))}
}

func transformDeleteDelete(a, b Op) []Op {
	from := shiftBackward(deleteFrom(a), b)
	to := shiftBackward(deleteTo(a), b)
	if from == to {
		// Everything a meant to delete, b had already deleted.
		return nil
	}
	return []Op{deleteRangeOp(a, from, to)}
}
