package document

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// applyOps applies ops to a copy of content, in order, and returns the result.
func applyOps(content string, ops []Op) string {
	b := New("/tmp/t", content)
	for _, op := range ops {
		b.Apply(op)
	}
	return b.Content()
}

// randomDoc builds a small document with a mix of line lengths, including empty
// lines and a trailing newline — the phantom-final-line case that trips up
// line/col arithmetic throughout this codebase.
//
// Multi-byte runes are in the alphabet deliberately: columns are rune counts
// everywhere in this package, so any place the transform reached for a byte
// length instead would show up here and nowhere in an ASCII-only corpus.
func randomDoc(rnd *rand.Rand) string {
	alphabet := []rune{'a', 'b', 'é', '日', '🙂', 'z'}
	lines := 1 + rnd.Intn(8)
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		for j := 0; j < rnd.Intn(8); j++ { // 0 makes an empty line
			sb.WriteRune(alphabet[rnd.Intn(len(alphabet))])
		}
		if i < lines-1 || rnd.Intn(2) == 0 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// randomPos picks a valid position in content, including end-of-line and the
// phantom line after a trailing newline.
func randomPos(rnd *rand.Rand, content string) Pos {
	b := New("/tmp/t", content)
	line := rnd.Intn(b.LineCount())
	return Pos{Line: line, Col: rnd.Intn(b.LineLen(line) + 1)}
}

// randomOp builds an insert or a delete at valid positions in content.
func randomOp(rnd *rand.Rand, content string, clientID uint64) Op {
	if rnd.Intn(2) == 0 {
		texts := []string{"x", "yz", "\n", "p\nq", "\nw", "ab\n", "m\n\nn", "é", "日本", "🙂\n🙂", "\n\n"}
		p := randomPos(rnd, content)
		return Op{
			ClientID: clientID, Type: OpInsert,
			InsertLine: p.Line, InsertCol: p.Col,
			InsertText: texts[rnd.Intn(len(texts))],
		}
	}
	p := randomPos(rnd, content)
	q := randomPos(rnd, content)
	if posLess(q, p) {
		p, q = q, p
	}
	return Op{
		ClientID: clientID, Type: OpDelete,
		FromLine: p.Line, FromCol: p.Col,
		ToLine: q.Line, ToCol: q.Col,
	}
}

// TestTransformTP1 is the property that makes convergence work, and the test
// worth more than every hand-written case combined:
//
//	apply(apply(doc, a), T(b, a)) == apply(apply(doc, b), T(a, b))
//
// Two clients each applying their own op and then the other's, transformed,
// must reach the same document. Randomized because the interesting failures are
// in the line/col arithmetic at boundaries — a position exactly at an insertion
// point, a delete ending exactly where another begins, an edit on the phantom
// line after a trailing newline — and those are tedious to enumerate by hand
// and easy to leave out.
func TestTransformTP1(t *testing.T) {
	rnd := rand.New(rand.NewSource(20260913))
	const iterations = 50000

	for i := 0; i < iterations; i++ {
		doc := randomDoc(rnd)
		a := randomOp(rnd, doc, 1)
		b := randomOp(rnd, doc, 2)

		// Client 1's path: its own op, then client 2's rebased onto it.
		// Client 2's path: the mirror. aWins is complementary, as the contract
		// requires, and is derived from the client ids so it is stable.
		left := applyOps(applyOps(doc, []Op{a}), Transform(b, a, false))
		right := applyOps(applyOps(doc, []Op{b}), Transform(a, b, true))

		if left != right {
			t.Fatalf("TP1 violated on iteration %d\n doc:   %q\n a:     %s\n b:     %s\n a→b→:  %q\n b→a→:  %q",
				i, doc, describe(a), describe(b), left, right)
		}
	}
}

func describe(op Op) string {
	switch op.Type {
	case OpInsert:
		return fmt.Sprintf("insert %q at %d:%d", op.InsertText, op.InsertLine, op.InsertCol)
	case OpDelete:
		return fmt.Sprintf("delete %d:%d-%d:%d", op.FromLine, op.FromCol, op.ToLine, op.ToCol)
	}
	return "noop"
}

// TestTransformPreservesConcurrentInsertInsideADelete pins the one case that
// produces two ops. Widening the delete to swallow the insert would be simpler
// and would silently destroy text the other client had just typed.
func TestTransformPreservesConcurrentInsertInsideADelete(t *testing.T) {
	doc := "abcdef\n"
	del := Op{ClientID: 1, Type: OpDelete, FromLine: 0, FromCol: 1, ToLine: 0, ToCol: 5} // "bcde"
	ins := Op{ClientID: 2, Type: OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "XY"}

	got := applyOps(applyOps(doc, []Op{ins}), Transform(del, ins, false))
	if !strings.Contains(got, "XY") {
		t.Errorf("result = %q, want the concurrently-inserted \"XY\" to survive the delete", got)
	}
	if got != "aXYf\n" {
		t.Errorf("result = %q, want %q", got, "aXYf\n")
	}

	// And it must still converge.
	other := applyOps(applyOps(doc, []Op{del}), Transform(ins, del, true))
	if got != other {
		t.Errorf("did not converge: %q vs %q", got, other)
	}
}

// TestTransformSplitOpsAreOrderedForSequentialApplication guards the ordering
// contract: the split returns the later range first so the earlier range's
// coordinates stay valid. Applying them in document order instead corrupts the
// result, which is exactly the kind of thing a reader might "tidy up".
func TestTransformSplitOpsAreOrderedForSequentialApplication(t *testing.T) {
	del := Op{ClientID: 1, Type: OpDelete, FromLine: 0, FromCol: 1, ToLine: 0, ToCol: 5}
	ins := Op{ClientID: 2, Type: OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "XY"}

	ops := Transform(del, ins, false)
	if len(ops) != 2 {
		t.Fatalf("got %d ops, want 2 (the delete must split around the insert)", len(ops))
	}
	// Later range first.
	if !posLess(deleteFrom(ops[1]), deleteFrom(ops[0])) {
		t.Errorf("split ops are in document order (%v then %v); the later range must come first "+
			"so the earlier one's coordinates survive its sibling being applied",
			deleteFrom(ops[0]), deleteFrom(ops[1]))
	}
}

// TestTransformFullyCancelledDeleteReturnsNothing covers a delete whose entire
// range another client had already removed.
func TestTransformFullyCancelledDeleteReturnsNothing(t *testing.T) {
	inner := Op{ClientID: 1, Type: OpDelete, FromLine: 0, FromCol: 2, ToLine: 0, ToCol: 4}
	outer := Op{ClientID: 2, Type: OpDelete, FromLine: 0, FromCol: 1, ToLine: 0, ToCol: 5}
	if ops := Transform(inner, outer, false); len(ops) != 0 {
		t.Errorf("got %d ops, want none — every character it meant to delete is already gone: %+v", len(ops), ops)
	}
}

// TestTransformTieBreakIsStableAndComplementary covers two inserts at the exact
// same position, where nothing in the content decides the order. Both sides
// must agree, which is what the complementary aWins contract buys.
func TestTransformTieBreakIsStableAndComplementary(t *testing.T) {
	doc := "ab\n"
	a := Op{ClientID: 1, Type: OpInsert, InsertLine: 0, InsertCol: 1, InsertText: "A"}
	b := Op{ClientID: 2, Type: OpInsert, InsertLine: 0, InsertCol: 1, InsertText: "B"}

	left := applyOps(applyOps(doc, []Op{a}), Transform(b, a, false))
	right := applyOps(applyOps(doc, []Op{b}), Transform(a, b, true))
	if left != right {
		t.Fatalf("same-position inserts did not converge: %q vs %q", left, right)
	}
	if left != "aABb\n" {
		t.Errorf("result = %q, want %q (lower client id first)", left, "aABb\n")
	}
}
