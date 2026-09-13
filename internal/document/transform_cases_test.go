package document

import "testing"

func ins(clientID uint64, line, col int, text string) Op {
	return Op{ClientID: clientID, Type: OpInsert, InsertLine: line, InsertCol: col, InsertText: text}
}

func del(clientID uint64, fl, fc, tl, tc int) Op {
	return Op{ClientID: clientID, Type: OpDelete, FromLine: fl, FromCol: fc, ToLine: tl, ToCol: tc}
}

// TestTransformAbsoluteOutcomes pins what the transform should actually produce,
// which the TP1 property test cannot: TP1 compares two paths against each other,
// so it passes just as happily when both are wrong in the same way. These cases
// assert the document, not merely agreement.
//
// Each case applies b, then a rebased onto b, and checks the result.
func TestTransformAbsoluteOutcomes(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		a, b  Op
		aWins bool
		want  string
	}{
		// ── insert / insert ──
		{
			name: "insert before another insert is untouched",
			doc:  "abc\n", a: ins(1, 0, 0, "X"), b: ins(2, 0, 2, "Y"),
			want: "XabYc\n",
		},
		{
			name: "insert after another insert shifts right",
			doc:  "abc\n", a: ins(1, 0, 2, "X"), b: ins(2, 0, 0, "Y"),
			want: "YabXc\n",
		},
		{
			name: "insert on a later line shifts down past added newlines",
			doc:  "ab\ncd\n", a: ins(1, 1, 1, "X"), b: ins(2, 0, 1, "\n"),
			want: "a\nb\ncXd\n",
		},

		// ── insert / delete ──
		{
			name: "insert after a delete shifts back",
			doc:  "abcdef\n", a: ins(1, 0, 5, "X"), b: del(2, 0, 1, 0, 3),
			want: "adeXf\n",
		},
		{
			name: "insert inside a deleted range collapses to its start",
			doc:  "abcdef\n", a: ins(1, 0, 2, "X"), b: del(2, 0, 1, 0, 4),
			want: "aXef\n",
		},

		// ── delete / insert ──
		{
			name: "delete after an insert shifts both ends forward",
			doc:  "abcdef\n", a: del(1, 0, 2, 0, 4), b: ins(2, 0, 0, "XY"),
			want: "XYabef\n",
		},
		{
			name: "insert exactly at the delete's end is not swallowed",
			doc:  "abcdef\n", a: del(1, 0, 1, 0, 3), b: ins(2, 0, 3, "X"),
			want: "aXdef\n",
		},
		{
			name: "insert exactly at the delete's start is not swallowed",
			doc:  "abcdef\n", a: del(1, 0, 2, 0, 4), b: ins(2, 0, 2, "X"),
			want: "abXef\n",
		},
		{
			name: "delete splits around an insert inside its range",
			doc:  "abcdef\n", a: del(1, 0, 1, 0, 5), b: ins(2, 0, 3, "XY"),
			want: "aXYf\n",
		},

		// ── delete / delete ──
		{
			name: "delete entirely before another is untouched",
			doc:  "abcdef\n", a: del(1, 0, 0, 0, 2), b: del(2, 0, 4, 0, 6),
			want: "cd\n",
		},
		{
			name: "delete entirely after another shifts back",
			doc:  "abcdef\n", a: del(1, 0, 4, 0, 6), b: del(2, 0, 0, 0, 2),
			want: "cd\n",
		},
		{
			name: "overlapping deletes remove only what is left",
			doc:  "abcdef\n", a: del(1, 0, 1, 0, 4), b: del(2, 0, 3, 0, 5),
			want: "af\n",
		},
		{
			name: "a delete containing another removes the remainder",
			doc:  "abcdef\n", a: del(1, 0, 1, 0, 5), b: del(2, 0, 2, 0, 4),
			want: "af\n",
		},
		{
			name: "a delete already fully covered becomes a no-op",
			doc:  "abcdef\n", a: del(1, 0, 2, 0, 4), b: del(2, 0, 1, 0, 5),
			want: "af\n",
		},
		{
			name: "multi-line delete after another shifts up",
			doc:  "a\nb\nc\nd\n", a: del(1, 2, 0, 3, 0), b: del(2, 0, 0, 1, 0),
			want: "b\nd\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			afterB := applyOps(tc.doc, []Op{tc.b})
			got := applyOps(afterB, Transform(tc.a, tc.b, tc.aWins))
			if got != tc.want {
				t.Errorf("doc %q\n  b   %s  -> %q\n  a   %s\n  got  %q\n  want %q",
					tc.doc, describe(tc.b), afterB, describe(tc.a), got, tc.want)
			}
			// Whatever the outcome, the mirror path must agree — otherwise the
			// expectation above is self-inconsistent.
			mirror := applyOps(applyOps(tc.doc, []Op{tc.a}), Transform(tc.b, tc.a, !tc.aWins))
			if mirror != got {
				t.Errorf("paths disagree: a-then-b %q vs b-then-a %q", mirror, got)
			}
		})
	}
}
