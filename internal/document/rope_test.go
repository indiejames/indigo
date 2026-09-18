package document

import (
	"math/rand"
	"strings"
	"testing"
)

func ropeDepth(r *ropeNode) int {
	if r == nil {
		return 0
	}
	if r.data != nil {
		return 1
	}
	return 1 + max(ropeDepth(r.left), ropeDepth(r.right))
}

func ropeLeaves(r *ropeNode) int {
	if r == nil {
		return 0
	}
	if r.data != nil {
		return 1
	}
	return ropeLeaves(r.left) + ropeLeaves(r.right)
}

// checkCachedFields verifies the values every traversal trusts without
// recomputing. A rebalance rebuilds every internal node, so a mistake there
// would not corrupt the text — it would make the tree lie about itself, and the
// lie surfaces later as a wrong line number rather than as an error here.
func checkCachedFields(t *testing.T, r *ropeNode) (runeLen, newlines, depth, leaves int) {
	t.Helper()
	if r == nil {
		return 0, 0, 0, 0
	}
	if r.data != nil {
		nl := strings.Count(string(r.data), "\n")
		if r.runeLen != len(r.data) || r.newlines != nl || r.depth != 1 || r.leaves != 1 {
			t.Fatalf("leaf caches wrong: runeLen=%d(want %d) newlines=%d(want %d) depth=%d leaves=%d",
				r.runeLen, len(r.data), r.newlines, nl, r.depth, r.leaves)
		}
		return len(r.data), nl, 1, 1
	}
	lr, ln, ld, ll := checkCachedFields(t, r.left)
	rr, rn, rd, rl := checkCachedFields(t, r.right)
	if r.runeLen != lr+rr || r.newlines != ln+rn || r.depth != 1+max(ld, rd) || r.leaves != ll+rl {
		t.Fatalf("internal caches wrong: runeLen=%d(want %d) newlines=%d(want %d) depth=%d(want %d) leaves=%d(want %d)",
			r.runeLen, lr+rr, r.newlines, ln+rn, r.depth, 1+max(ld, rd), r.leaves, ll+rl)
	}
	return r.runeLen, r.newlines, r.depth, r.leaves
}

// TestRopeStaysBalancedUnderScatteredEdits is the regression test.
//
// ropeConcat hangs a new node off the root and an edit is split-then-concat, so
// before rebalancing every edit at a fresh location added a level. Measured
// before the fix: 2000 scattered edits over a 6800-rune buffer gave depth 2007
// where balanced is 12, with 2064 leaves averaging three runes each. The tree
// had become a list, and since every traversal is O(depth) — including
// newlinesBefore and offsetOfNthNewline, both on the render path — a long
// editing session degraded steadily with nothing to recover it.
//
// The edits alternate between two distant offsets on purpose: the gap buffer
// absorbs *contiguous* typing, so it is moving between edit sites that forces a
// flush into the rope. That is ordinary editing, not a pathological case.
func TestRopeStaysBalancedUnderScatteredEdits(t *testing.T) {
	b := New("x.go", strings.Repeat("hello world\n", 400))
	for i := 0; i < 2000; i++ {
		off := 10
		if i%2 == 1 {
			off = b.totalLen() - 10
		}
		p := b.PosFromOffset(off)
		b.Apply(Op{Type: OpInsert, InsertLine: p.Line, InsertCol: p.Col, InsertText: "z"})
	}

	depth, leaves := ropeDepth(b.rope), ropeLeaves(b.rope)
	// Generous: the bound ropeConcat enforces is 2*idealDepth+2, and the gap
	// holds content back from the rope, so assert the property (logarithmic)
	// rather than the exact number. Anything near the edit count is the bug.
	if limit := 4 * idealDepth(leaves); depth > limit {
		t.Errorf("depth = %d over %d leaves, want <= %d — the tree is degenerating "+
			"toward a list", depth, leaves, limit)
	}
	if depth > 60 {
		t.Errorf("depth = %d after 2000 edits: that tracks the edit count, not the content", depth)
	}
	checkCachedFields(t, b.rope)
}

// TestRopeRebalancePreservesContent is the correctness half. Rebalancing
// restructures the tree under everything that reads it, so the text, the line
// count and every line's content must come through untouched.
func TestRopeRebalancePreservesContent(t *testing.T) {
	var want strings.Builder
	for i := 0; i < 300; i++ {
		want.WriteString("line ")
		want.WriteByte(byte('a' + i%26))
		want.WriteString("\n")
	}
	b := New("x.go", want.String())
	before := b.Content()

	// Force many rebalances, then confirm nothing moved.
	for i := 0; i < 1500; i++ {
		off := 3
		if i%2 == 1 {
			off = b.totalLen() - 3
		}
		p := b.PosFromOffset(off)
		b.Apply(Op{Type: OpInsert, InsertLine: p.Line, InsertCol: p.Col, InsertText: "Q"})
		b.Apply(Op{Type: OpDelete, FromLine: p.Line, FromCol: p.Col, ToLine: p.Line, ToCol: p.Col + 1})
	}

	if got := b.Content(); got != before {
		t.Errorf("content changed across rebalancing: %d runes vs %d", len([]rune(got)), len([]rune(before)))
	}
	if got, w := b.LineCount(), strings.Count(before, "\n")+1; got != w {
		t.Errorf("LineCount = %d, want %d", got, w)
	}
	wantLines := strings.Split(before, "\n")
	for i := range wantLines {
		if got := b.Line(i); got != wantLines[i] {
			t.Fatalf("Line(%d) = %q, want %q", i, got, wantLines[i])
		}
	}
	checkCachedFields(t, b.rope)
}

// TestRopeMatchesAStringModelUnderRandomEdits is the property test: the buffer
// must behave exactly like the obvious string implementation, whatever the
// rebalancer does underneath. A seeded sequence so a failure is reproducible.
func TestRopeMatchesAStringModelUnderRandomEdits(t *testing.T) {
	rng := rand.New(rand.NewSource(20260916))
	const alphabet = "ab\ncd\nef"

	model := []rune("first\nsecond\nthird\n")
	b := New("x.go", string(model))

	for step := 0; step < 4000; step++ {
		if len(model) > 0 && rng.Intn(3) == 0 {
			from := rng.Intn(len(model))
			to := from + 1 + rng.Intn(min(20, len(model)-from))
			fp, tp := b.PosFromOffset(from), b.PosFromOffset(to)
			b.Apply(Op{Type: OpDelete, FromLine: fp.Line, FromCol: fp.Col, ToLine: tp.Line, ToCol: tp.Col})
			model = append(model[:from], model[to:]...)
		} else {
			at := rng.Intn(len(model) + 1)
			n := 1 + rng.Intn(8)
			ins := make([]rune, n)
			for i := range ins {
				ins[i] = rune(alphabet[rng.Intn(len(alphabet))])
			}
			p := b.PosFromOffset(at)
			b.Apply(Op{Type: OpInsert, InsertLine: p.Line, InsertCol: p.Col, InsertText: string(ins)})
			model = append(model[:at], append(append([]rune{}, ins...), model[at:]...)...)
		}

		if step%250 != 0 {
			continue
		}
		if got := b.Content(); got != string(model) {
			t.Fatalf("step %d: content diverged\n got %q\nwant %q", step, got, string(model))
		}
		if got, want := b.LineCount(), strings.Count(string(model), "\n")+1; got != want {
			t.Fatalf("step %d: LineCount = %d, want %d", step, got, want)
		}
	}

	if got := b.Content(); got != string(model) {
		t.Fatalf("final content diverged:\n got %q\nwant %q", got, string(model))
	}
	checkCachedFields(t, b.rope)
}

// TestCoalesceLeavesMergesWithoutReorderingOrCopyingFullLeaves pins the two
// properties coalescing has to have: order (and therefore content) is
// preserved, and a leaf already at the size limit is passed through by pointer
// rather than copied, so rebalancing a large well-formed rope moves no text.
func TestCoalesceLeavesMergesWithoutReorderingOrCopyingFullLeaves(t *testing.T) {
	full := newLeaf([]rune(strings.Repeat("x", leafMaxRunes)))
	in := []*ropeNode{
		newLeaf([]rune("ab")),
		newLeaf([]rune("cd")),
		full,
		newLeaf([]rune("ef")),
		newLeaf(nil), // empty leaves are dropped
		newLeaf([]rune("gh")),
	}

	out := coalesceLeaves(in)

	var got strings.Builder
	for _, lf := range out {
		got.WriteString(string(lf.data))
	}
	if want := "abcd" + strings.Repeat("x", leafMaxRunes) + "efgh"; got.String() != want {
		t.Errorf("coalesced content = %q, want %q", got.String(), want)
	}
	if len(out) != 3 {
		t.Errorf("got %d leaves, want 3 (merged, full, merged): %d", len(out), len(out))
	}
	if out[1] != full {
		t.Error("a leaf already at the size limit must be reused by pointer, not copied")
	}
}
