package document

import (
	"math/rand"
	"strings"
	"testing"
)

func applyAll(t *testing.T, a string, ops []Op) string {
	t.Helper()
	b := New("", a)
	for _, op := range ops {
		b.Apply(op)
	}
	return b.Content()
}

func TestDiffOps(t *testing.T) {
	cases := []struct{ name, a, b string }{
		{"equal", "x\n", "x\n"},
		{"empty to text", "", "hello\n"},
		{"text to empty", "hello\n", ""},
		{"reindent", "func f() {\nreturn 1\n}\n", "func f() {\n\treturn 1\n}\n"},
		{"add trailing newline", "a\nb", "a\nb\n"},
		{"drop trailing newline", "a\nb\n", "a\nb"},
		{"insert line", "a\nc\n", "a\nb\nc\n"},
		{"delete line", "a\nb\nc\n", "a\nc\n"},
		{"multibyte", "héllo wörld\n", "héllo, wörld\n"},
		{"several hunks", "a\nb\nc\nd\ne\nf\n", "A\nb\nc\nD\ne\nF\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ops := DiffOps(c.a, c.b)
			if got := applyAll(t, c.a, ops); got != c.b {
				t.Fatalf("applying %v to %q gave %q, want %q", ops, c.a, got, c.b)
			}
		})
	}
}

// Reindenting one line must touch only its indentation: a cursor anywhere
// else, or a concurrent edit elsewhere on the line, is then left alone.
func TestDiffOpsIsNarrow(t *testing.T) {
	ops := DiffOps("a\nreturn 1\nb\n", "a\n\treturn 1\nb\n")
	if len(ops) != 1 || ops[0].Type != OpInsert || ops[0].InsertLine != 1 ||
		ops[0].InsertCol != 0 || ops[0].InsertText != "\t" {
		t.Fatalf("ops = %+v, want one tab inserted at 1:0", ops)
	}
}

func TestDiffOpsRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	words := []string{"a", "b", "func", "\t", " ", "é", "\n", "\n", "{", "}"}
	gen := func() string {
		var sb strings.Builder
		for range rng.Intn(40) {
			sb.WriteString(words[rng.Intn(len(words))])
		}
		return sb.String()
	}
	mutate := func(s string) string {
		lines := strings.SplitAfter(s, "\n")
		for i := range lines {
			if rng.Intn(3) == 0 {
				lines[i] = gen()
			}
		}
		return strings.Join(lines, "")
	}
	for i := range 3000 {
		a := gen()
		b := mutate(a)
		if i%5 == 0 {
			b = gen()
		}
		if got := applyAll(t, a, DiffOps(a, b)); got != b {
			t.Fatalf("seed case %d: %q -> %q gave %q", i, a, b, got)
		}
	}
}

// Past maxDiffD the diff falls back to one coarse hunk, which must still be
// correct.
func TestDiffOpsFallback(t *testing.T) {
	var a, b strings.Builder
	for i := range 3 * maxDiffD {
		a.WriteString("x\n")
		if i%2 == 0 {
			b.WriteString("y\n")
		} else {
			b.WriteString("x\n")
		}
	}
	if got := applyAll(t, a.String(), DiffOps(a.String(), b.String())); got != b.String() {
		t.Fatal("fallback diff produced the wrong content")
	}
}
