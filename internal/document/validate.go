package document

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// CheckOps reports the first op in ops whose position does not exist in the
// buffer as it will be when that op applies: each op is checked against the
// buffer with every op before it in the list already applied. It changes
// nothing.
//
// Apply clamps a position past the end of a line or the buffer to the nearest
// one that exists, and so never fails. That is right for a window applying its
// own edits, where the position came from its own cursor. It is wrong for one
// that arrived from elsewhere: an op out of range was computed against text
// the buffer does not hold, and clamping it puts the edit somewhere nobody
// asked for, with nothing to say so. The server checks with this first and
// refuses instead, which a window answers by resyncing.
func (b *Buffer) CheckOps(ops []Op) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(ops) == 1 {
		// The common case, one keystroke, checked without copying anything.
		return checkOp(ops[0], b.lineCount(), b.internalLineLen)
	}
	lens := make([]int, b.lineCount())
	for i := range lens {
		lens[i] = b.internalLineLen(i)
	}
	for i, op := range ops {
		if err := checkOp(op, len(lens), func(n int) int { return lens[n] }); err != nil {
			return fmt.Errorf("op %d of %d: %w", i+1, len(ops), err)
		}
		lens = applyToLineLens(lens, op)
	}
	return nil
}

func checkOp(op Op, lineCount int, lineLen func(int) int) error {
	inRange := func(line, col int) bool {
		return line >= 0 && line < lineCount && col >= 0 && col <= lineLen(line)
	}
	switch op.Type {
	case OpInsert:
		if !inRange(op.InsertLine, op.InsertCol) {
			return fmt.Errorf("insert at %d:%d is outside the buffer (%d lines)", op.InsertLine, op.InsertCol, lineCount)
		}
	case OpDelete:
		if !inRange(op.FromLine, op.FromCol) || !inRange(op.ToLine, op.ToCol) {
			return fmt.Errorf("delete of %d:%d-%d:%d is outside the buffer (%d lines)",
				op.FromLine, op.FromCol, op.ToLine, op.ToCol, lineCount)
		}
	default:
		return fmt.Errorf("unknown op type %d", op.Type)
	}
	return nil
}

// applyToLineLens returns lens, the length of each line, as it is after op.
// op must already have been checked against lens.
func applyToLineLens(lens []int, op Op) []int {
	switch op.Type {
	case OpInsert:
		parts := strings.Split(op.InsertText, "\n")
		line, col := op.InsertLine, op.InsertCol
		if len(parts) == 1 {
			lens[line] += utf8.RuneCountInString(parts[0])
			return lens
		}
		after := lens[line] - col
		added := make([]int, len(parts))
		added[0] = col + utf8.RuneCountInString(parts[0])
		for i := 1; i < len(parts)-1; i++ {
			added[i] = utf8.RuneCountInString(parts[i])
		}
		added[len(parts)-1] = utf8.RuneCountInString(parts[len(parts)-1]) + after
		out := make([]int, 0, len(lens)+len(parts)-1)
		out = append(out, lens[:line]...)
		out = append(out, added...)
		return append(out, lens[line+1:]...)
	case OpDelete:
		fl, fc, tl, tc := op.FromLine, op.FromCol, op.ToLine, op.ToCol
		if fl > tl || (fl == tl && fc > tc) {
			fl, fc, tl, tc = tl, tc, fl, fc // Apply accepts either order
		}
		lens[fl] = fc + lens[tl] - tc
		return append(lens[:fl+1], lens[tl+1:]...)
	}
	return lens
}
