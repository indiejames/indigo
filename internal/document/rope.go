package document

import "math/bits"

const leafMaxRunes = 128

// ropeNode is a node in an immutable rope tree.
// Leaves have data != nil; internal nodes have left and right children.
// runeLen, newlines, depth and leaves are cached for the entire subtree.
type ropeNode struct {
	left, right *ropeNode
	runeLen     int
	newlines    int
	depth       int    // 1 for a leaf; 1 + max(children) for an internal node
	leaves      int    // leaf count of this subtree
	data        []rune // non-nil only for leaves
}

func newLeaf(data []rune) *ropeNode {
	nl := 0
	for _, r := range data {
		if r == '\n' {
			nl++
		}
	}
	return &ropeNode{runeLen: len(data), newlines: nl, depth: 1, leaves: 1, data: data}
}

func newInternal(left, right *ropeNode) *ropeNode {
	return &ropeNode{
		left:     left,
		right:    right,
		runeLen:  left.runeLen + right.runeLen,
		newlines: left.newlines + right.newlines,
		depth:    1 + max(left.depth, right.depth),
		leaves:   left.leaves + right.leaves,
	}
}

// idealDepth is the depth of a perfectly balanced tree over n leaves.
func idealDepth(n int) int {
	if n <= 1 {
		return 1
	}
	return bits.Len(uint(n-1)) + 1
}

// ropeNeedsRebalance reports whether r has drifted far enough from balanced to
// be worth rebuilding.
//
// The allowance is generous — twice the ideal depth plus two — because
// rebuilding is O(leaves) and the point is to bound the worst case, not to keep
// the tree pretty. Every concat can add one level at the root, so this permits
// roughly idealDepth concats between rebuilds and the rebuild cost amortizes
// over them.
func ropeNeedsRebalance(r *ropeNode) bool {
	return r != nil && r.data == nil && r.depth > 2*idealDepth(r.leaves)+2
}

// ropeRebalance rebuilds r as a balanced tree over its leaves.
//
// Without this the tree degenerates into a list. ropeConcat hangs a new node off
// the root, and an edit is split-then-concat, so each edit at a fresh location
// adds a level: 2000 scattered edits produced depth 2007 where balanced is 12.
// Every traversal here is O(depth) — newlinesBefore and offsetOfNthNewline run
// on the render path — so a long editing session degraded steadily, and nothing
// ever recovered because nothing ever rebuilt.
//
// Leaves are reused rather than copied: they are immutable, so only the
// internal nodes are rebuilt. The exception is coalescing, which is worth the
// copy — the same fragmentation that deepens the tree also shatters it into
// tiny leaves (2064 leaves averaging 3 runes each in that measurement), and
// leaving those in place would mean rebalancing again almost immediately.
func ropeRebalance(r *ropeNode) *ropeNode {
	if r == nil {
		return nil
	}
	leaves := make([]*ropeNode, 0, r.leaves)
	collectLeaves(r, &leaves)
	return buildBalanced(coalesceLeaves(leaves))
}

func collectLeaves(r *ropeNode, out *[]*ropeNode) {
	if r == nil {
		return
	}
	if r.data != nil {
		*out = append(*out, r)
		return
	}
	collectLeaves(r.left, out)
	collectLeaves(r.right, out)
}

// coalesceLeaves merges adjacent undersized leaves up to leafMaxRunes,
// preserving order and therefore content.
//
// Accumulating into one pending slice rather than merging pairwise matters:
// merging k tiny leaves a pair at a time recopies the prefix every time, which
// is quadratic in k, and k reaches leafMaxRunes in exactly the fragmented case
// this exists to clean up. A leaf already at or over the limit is passed
// through by pointer, so a large well-formed rope is rebalanced without copying
// any text at all.
func coalesceLeaves(leaves []*ropeNode) []*ropeNode {
	out := make([]*ropeNode, 0, len(leaves))
	var pending []rune
	flush := func() {
		if len(pending) > 0 {
			out = append(out, newLeaf(pending))
			pending = nil
		}
	}
	for _, lf := range leaves {
		if lf.runeLen == 0 {
			continue
		}
		if lf.runeLen >= leafMaxRunes {
			flush()
			out = append(out, lf)
			continue
		}
		if len(pending)+lf.runeLen > leafMaxRunes {
			flush()
		}
		pending = append(pending, lf.data...)
	}
	flush()
	return out
}

// buildBalanced builds a balanced tree over leaves, which must be in document
// order. It calls newInternal directly rather than ropeConcat: the result is
// balanced by construction, so re-checking it at every join would be wasted
// work, and ropeConcat's leaf merging would undo coalesceLeaves' arrangement.
func buildBalanced(leaves []*ropeNode) *ropeNode {
	switch len(leaves) {
	case 0:
		return nil
	case 1:
		return leaves[0]
	}
	mid := len(leaves) / 2
	return newInternal(buildBalanced(leaves[:mid]), buildBalanced(leaves[mid:]))
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
	n := newInternal(a, b)
	// The one place a tree grows a level, so the one place that has to notice
	// it has grown too many.
	if ropeNeedsRebalance(n) {
		return ropeRebalance(n)
	}
	return n
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
