package document

const leafMaxRunes = 128

// ropeNode is a node in an immutable rope tree.
// Leaves have data != nil; internal nodes have left and right children.
// runeLen and newlines are cached for the entire subtree.
type ropeNode struct {
	left, right *ropeNode
	runeLen     int
	newlines    int
	data        []rune // non-nil only for leaves
}

func newLeaf(data []rune) *ropeNode {
	nl := 0
	for _, r := range data {
		if r == '\n' {
			nl++
		}
	}
	return &ropeNode{runeLen: len(data), newlines: nl, data: data}
}

func newInternal(left, right *ropeNode) *ropeNode {
	return &ropeNode{
		left:     left,
		right:    right,
		runeLen:  left.runeLen + right.runeLen,
		newlines: left.newlines + right.newlines,
	}
}

// ropeFromRunes builds a balanced rope from a rune slice. Returns nil for empty input.
func ropeFromRunes(runes []rune) *ropeNode {
	if len(runes) == 0 {
		return nil
	}
	if len(runes) <= leafMaxRunes {
		cp := make([]rune, len(runes))
		copy(cp, runes)
		return newLeaf(cp)
	}
	mid := len(runes) / 2
	return newInternal(ropeFromRunes(runes[:mid]), ropeFromRunes(runes[mid:]))
}

// ropeConcat concatenates two ropes, merging small adjacent leaves.
func ropeConcat(a, b *ropeNode) *ropeNode {
	if a == nil || a.runeLen == 0 {
		return b
	}
	if b == nil || b.runeLen == 0 {
		return a
	}
	if a.data != nil && b.data != nil && a.runeLen+b.runeLen <= leafMaxRunes {
		merged := make([]rune, a.runeLen+b.runeLen)
		copy(merged, a.data)
		copy(merged[a.runeLen:], b.data)
		return newLeaf(merged)
	}
	return newInternal(a, b)
}

// ropeSplit splits the rope at flat rune offset i, returning (left[0..i), right[i..]).
func ropeSplit(r *ropeNode, i int) (*ropeNode, *ropeNode) {
	if r == nil || i <= 0 {
		return nil, r
	}
	if i >= r.runeLen {
		return r, nil
	}
	if r.data != nil {
		ldata := make([]rune, i)
		copy(ldata, r.data[:i])
		rdata := make([]rune, len(r.data)-i)
		copy(rdata, r.data[i:])
		var ln, rn *ropeNode
		if len(ldata) > 0 {
			ln = newLeaf(ldata)
		}
		if len(rdata) > 0 {
			rn = newLeaf(rdata)
		}
		return ln, rn
	}
	if i <= r.left.runeLen {
		ll, lr := ropeSplit(r.left, i)
		return ll, ropeConcat(lr, r.right)
	}
	rl, rr := ropeSplit(r.right, i-r.left.runeLen)
	return ropeConcat(r.left, rl), rr
}

// ropeInsert inserts runes at flat offset at.
func ropeInsert(r *ropeNode, at int, runes []rune) *ropeNode {
	if len(runes) == 0 {
		return r
	}
	left, right := ropeSplit(r, at)
	return ropeConcat(ropeConcat(left, ropeFromRunes(runes)), right)
}

// ropeDelete deletes the range [from, to) from the rope.
func ropeDelete(r *ropeNode, from, to int) *ropeNode {
	if r == nil || from >= to {
		return r
	}
	left, rest := ropeSplit(r, from)
	_, right := ropeSplit(rest, to-from)
	return ropeConcat(left, right)
}

// newlinesBefore counts '\n' in r[0..at).
func (r *ropeNode) newlinesBefore(at int) int {
	if r == nil || at <= 0 {
		return 0
	}
	if at >= r.runeLen {
		return r.newlines
	}
	if r.data != nil {
		count := 0
		for _, ch := range r.data[:at] {
			if ch == '\n' {
				count++
			}
		}
		return count
	}
	if at <= r.left.runeLen {
		return r.left.newlinesBefore(at)
	}
	return r.left.newlines + r.right.newlinesBefore(at-r.left.runeLen)
}

// offsetOfNthNewline returns the flat offset of the n-th '\n' (0-indexed), or -1 if not found.
func (r *ropeNode) offsetOfNthNewline(n int) int {
	if r == nil || n < 0 || n >= r.newlines {
		return -1
	}
	if r.data != nil {
		count := 0
		for i, ch := range r.data {
			if ch == '\n' {
				if count == n {
					return i
				}
				count++
			}
		}
		return -1
	}
	if r.left.newlines > n {
		return r.left.offsetOfNthNewline(n)
	}
	off := r.right.offsetOfNthNewline(n - r.left.newlines)
	if off < 0 {
		return -1
	}
	return r.left.runeLen + off
}

// extractRange returns runes in [from, to) from the rope.
func (r *ropeNode) extractRange(from, to int) []rune {
	if r == nil || from >= to {
		return nil
	}
	from = max(from, 0)
	to = min(to, r.runeLen)
	if from >= to {
		return nil
	}
	out := make([]rune, 0, to-from)
	r.collectRange(from, to, &out)
	return out
}

func (r *ropeNode) collectRange(from, to int, out *[]rune) {
	if r == nil || from >= r.runeLen || from >= to {
		return
	}
	if r.data != nil {
		start := max(from, 0)
		end := min(to, len(r.data))
		if start < end {
			*out = append(*out, r.data[start:end]...)
		}
		return
	}
	leftLen := r.left.runeLen
	if from < leftLen {
		r.left.collectRange(from, min(to, leftLen), out)
	}
	if to > leftLen {
		r.right.collectRange(max(0, from-leftLen), to-leftLen, out)
	}
}

// ropeLen returns 0 for a nil rope.
func ropeLen(r *ropeNode) int {
	if r == nil {
		return 0
	}
	return r.runeLen
}

// ropeNewlines returns 0 for a nil rope.
func ropeNewlines(r *ropeNode) int {
	if r == nil {
		return 0
	}
	return r.newlines
}
