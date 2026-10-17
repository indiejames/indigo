package document

import (
	"sort"
	"strings"
)

// DiffOps returns ops that turn a into b when applied in order, each against
// the document the previous one left.
//
// It exists so a formatter's output can reach a buffer as ordinary edits
// rather than as a replacement buffer. A replacement has to bump the buffer's
// generation, and every op computed against the old buffer — a keystroke typed
// while the formatter ran, in this window or another — is then refused and
// lost. Ops go through operational transform like anything else, so concurrent
// edits are rebased past them instead, and cursors in every window move with
// the text rather than being clamped.
//
// The diff is by line (Myers), then each changed hunk is narrowed to the runes
// that actually differ, so reindenting a line touches only its indentation.
// Ops are emitted last hunk first, which keeps each one's coordinates valid in
// a: nothing before a hunk has changed yet when it is applied.
//
// Within a hunk the insert comes before the delete. The order decides where
// text typed concurrently at a hunk's edges ends up once rebased: typed at its
// start, it ties with the insert and stays in front of the new text; typed at
// its end, it sits past the deleted range and stays behind it. Delete-first
// collapses the range to a point, both edges tie with the insert, and text
// typed after the old content landed in front of the new.
func DiffOps(a, b string) []Op {
	if a == b {
		return nil
	}
	al, bl := splitKeepNewlines(a), splitKeepNewlines(b)

	pre := 0
	for pre < len(al) && pre < len(bl) && al[pre] == bl[pre] {
		pre++
	}
	suf := 0
	for suf < len(al)-pre && suf < len(bl)-pre && al[len(al)-1-suf] == bl[len(bl)-1-suf] {
		suf++
	}
	hunks := lineHunks(al[pre:len(al)-suf], bl[pre:len(bl)-suf])

	starts := lineStarts(al, a)
	var ops []Op
	for i := len(hunks) - 1; i >= 0; i-- {
		h := hunks[i]
		h.a0, h.a1, h.b0, h.b1 = h.a0+pre, h.a1+pre, h.b0+pre, h.b1+pre
		oldRunes := []rune(strings.Join(al[h.a0:h.a1], ""))
		newRunes := []rune(strings.Join(bl[h.b0:h.b1], ""))
		p := 0
		for p < len(oldRunes) && p < len(newRunes) && oldRunes[p] == newRunes[p] {
			p++
		}
		s := 0
		for s < len(oldRunes)-p && s < len(newRunes)-p &&
			oldRunes[len(oldRunes)-1-s] == newRunes[len(newRunes)-1-s] {
			s++
		}
		from := starts[h.a0] + p
		to := starts[h.a0] + len(oldRunes) - s
		fl, fc := posAt(starts, from)
		tl, tc := posAt(starts, to)
		if ins := string(newRunes[p : len(newRunes)-s]); ins != "" {
			ops = append(ops, Op{Type: OpInsert, InsertLine: fl, InsertCol: fc, InsertText: ins})
			// The deleted range now starts where the insert ended.
			el, ec := fl, fc
			for _, r := range ins {
				if r == '\n' {
					el++
					ec = 0
				} else {
					ec++
				}
			}
			if tl == fl {
				tc += ec - fc
			}
			tl += el - fl
			fl, fc = el, ec
		}
		if to > from {
			ops = append(ops, Op{Type: OpDelete, FromLine: fl, FromCol: fc, ToLine: tl, ToCol: tc})
		}
	}
	return ops
}

// splitKeepNewlines splits s into lines that each keep their "\n", so joining
// them gives s back exactly. A trailing "\n" does not produce an empty element.
func splitKeepNewlines(s string) []string {
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lineStarts gives the rune offset at which each line of a begins. When a is
// empty or ends in "\n" there is one more entry, for the phantom empty final
// line the buffer counts (see Buffer.LineCount).
func lineStarts(lines []string, a string) []int {
	starts := make([]int, 0, len(lines)+1)
	off := 0
	for _, l := range lines {
		starts = append(starts, off)
		off += len([]rune(l))
	}
	if a == "" || strings.HasSuffix(a, "\n") {
		starts = append(starts, off)
	}
	return starts
}

// posAt converts a rune offset into a line and column.
func posAt(starts []int, off int) (line, col int) {
	line = sort.Search(len(starts), func(i int) bool { return starts[i] > off }) - 1
	return line, off - starts[line]
}

type hunk struct{ a0, a1, b0, b1 int }

// maxDiffD bounds the Myers search. The trace it keeps grows with the square
// of the edit distance, so a file rewritten almost entirely falls back to one
// hunk covering everything between the unchanged first and last lines — still
// correct, just coarser.
const maxDiffD = 1000

// lineHunks returns the changed regions between a and b, in order.
func lineHunks(a, b []string) []hunk {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	steps, ok := myers(a, b)
	if !ok {
		return []hunk{{0, len(a), 0, len(b)}}
	}
	var hunks []hunk
	x, y := 0, 0
	open := false
	var cur hunk
	for _, st := range steps {
		if st == stepEqual {
			if open {
				cur.a1, cur.b1 = x, y
				hunks = append(hunks, cur)
				open = false
			}
			x++
			y++
			continue
		}
		if !open {
			cur = hunk{a0: x, b0: y}
			open = true
		}
		if st == stepDelete {
			x++
		} else {
			y++
		}
	}
	if open {
		cur.a1, cur.b1 = x, y
		hunks = append(hunks, cur)
	}
	return hunks
}

type diffStep uint8

const (
	stepEqual diffStep = iota
	stepDelete
	stepInsert
)

// myers is the greedy O((N+M)D) algorithm from "An O(ND) Difference Algorithm
// and Its Variations" (Myers, 1986), returning the edit script in order. It
// reports false when the edit distance exceeds maxDiffD.
func myers(a, b []string) ([]diffStep, bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxDiffD)
	off := limit + 1
	v := make([]int, 2*limit+3)
	// trace[d] holds v[k] for k in [-d, d] once step d is done.
	var trace [][]int
	for d := 0; d <= limit; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				trace = append(trace, append([]int(nil), v[off-d:off+d+1]...))
				return backtrack(trace, n, m), true
			}
		}
		trace = append(trace, append([]int(nil), v[off-d:off+d+1]...))
	}
	return nil, false
}

func backtrack(trace [][]int, n, m int) []diffStep {
	var steps []diffStep
	x, y := n, m
	for d := len(trace) - 1; d > 0; d-- {
		prev := trace[d-1]
		at := func(k int) int { return prev[k+d-1] }
		k := x - y
		var pk int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := at(pk)
		py := px - pk
		// One step off the previous diagonal, then a snake of equal lines.
		mx, my := px+1, py
		if pk == k+1 {
			mx, my = px, py+1
		}
		for x > mx && y > my {
			steps = append(steps, stepEqual)
			x--
			y--
		}
		if pk == k+1 {
			steps = append(steps, stepInsert)
		} else {
			steps = append(steps, stepDelete)
		}
		x, y = px, py
	}
	for x > 0 && y > 0 {
		steps = append(steps, stepEqual)
		x--
		y--
	}
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps
}
