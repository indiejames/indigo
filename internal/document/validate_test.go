package document

import (
	"math/rand"
	"strings"
	"testing"
)

func TestCheckOpsRejectsPositionsOutsideTheBuffer(t *testing.T) {
	b := New("", "ab\ncd\n") // lines "ab", "cd", ""
	for _, tc := range []struct {
		name string
		ops  []Op
	}{
		{"line past the end", []Op{{Type: OpInsert, InsertLine: 3, InsertText: "x"}}},
		{"column past the end of the line", []Op{{Type: OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "x"}}},
		{"negative column", []Op{{Type: OpInsert, InsertLine: 0, InsertCol: -1, InsertText: "x"}}},
		{"delete ending past its line", []Op{{Type: OpDelete, FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 5}}},
		// Valid against the buffer, not against it after the first op: the
		// delete removed the line the insert names.
		{"valid only before an earlier op", []Op{
			{Type: OpDelete, FromLine: 0, FromCol: 2, ToLine: 1, ToCol: 2},
			{Type: OpInsert, InsertLine: 2, InsertCol: 0, InsertText: "x"},
		}},
	} {
		if err := b.CheckOps(tc.ops); err == nil {
			t.Errorf("%s: CheckOps accepted %+v", tc.name, tc.ops)
		}
	}
	ok := []Op{
		{Type: OpInsert, InsertLine: 2, InsertCol: 0, InsertText: "end"},
		{Type: OpDelete, FromLine: 1, FromCol: 2, ToLine: 0, ToCol: 1}, // reversed, as Apply accepts
		{Type: OpInsert, InsertLine: 1, InsertCol: 3, InsertText: "!"},
	}
	if err := b.CheckOps(ok); err != nil {
		t.Errorf("CheckOps refused a valid sequence: %v", err)
	}
	if b.Content() != "ab\ncd\n" {
		t.Error("CheckOps changed the buffer")
	}
}

// TestCheckOpsAgreesWithApply checks the line-length model CheckOps keeps for
// a batch against what Apply actually does, over random edits: every op that
// really is in range must be accepted, with the ops before it in the batch
// applied.
func TestCheckOpsAgreesWithApply(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"a", "é", "\n", "xy", "\n\n", "z\nq"}
	for iter := 0; iter < 2000; iter++ {
		start := ""
		for i := rng.Intn(6); i > 0; i-- {
			start += alphabet[rng.Intn(len(alphabet))]
		}
		b := New("", start)
		check := New("", start)
		var ops []Op
		for i := 1 + rng.Intn(5); i > 0; i-- {
			op := randomValidOp(rng, b, alphabet)
			b.Apply(op)
			ops = append(ops, op)
		}
		if err := check.CheckOps(ops); err != nil {
			t.Fatalf("start %q, ops %+v: %v", start, ops, err)
		}
		lens := make([]int, check.LineCount())
		for i := range lens {
			lens[i] = check.LineLen(i)
		}
		for _, op := range ops {
			lens = applyToLineLens(lens, op)
		}
		got := strings.Split(b.Content(), "\n")
		if len(lens) != len(got) {
			t.Fatalf("start %q, ops %+v: model has %d lines, buffer %d", start, ops, len(lens), len(got))
		}
		for i := range got {
			if lens[i] != len([]rune(got[i])) {
				t.Fatalf("start %q, ops %+v: line %d model %d, buffer %d", start, ops, i, lens[i], len([]rune(got[i])))
			}
		}
	}
}

func randomValidOp(rng *rand.Rand, b *Buffer, alphabet []string) Op {
	pos := func() (int, int) {
		line := rng.Intn(b.LineCount())
		return line, rng.Intn(b.LineLen(line) + 1)
	}
	if rng.Intn(2) == 0 {
		l, c := pos()
		return Op{Type: OpInsert, InsertLine: l, InsertCol: c, InsertText: alphabet[rng.Intn(len(alphabet))]}
	}
	fl, fc := pos()
	tl, tc := pos()
	return Op{Type: OpDelete, FromLine: fl, FromCol: fc, ToLine: tl, ToCol: tc}
}
