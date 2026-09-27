package debug

import (
	"fmt"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func lines(b *Breakpoints, path string) string {
	bps, _ := b.List(path)
	out := ""
	for i, bp := range bps {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprint(bp.Line)
	}
	return out
}

func TestToggleAndList(t *testing.T) {
	b := NewBreakpoints()
	if !b.Toggle("/w/a.go", 10) || !b.Toggle("/w/a.go", 3) || !b.Toggle("/w/b.go", 1) {
		t.Fatal("Toggle on an empty line should report set")
	}
	if got := lines(b, "/w/a.go"); got != "3,10" {
		t.Errorf("a.go = %s, want 3,10 (sorted)", got)
	}
	if b.Toggle("/w/a.go", 10) {
		t.Error("Toggle on a set line should remove it and report unset")
	}
	all, _ := b.List("")
	if len(all) != 2 || all[0].Path != "/w/a.go" || all[1].Path != "/w/b.go" {
		t.Errorf("List(\"\") = %+v", all)
	}
}

func TestSeqMovesOnEveryChange(t *testing.T) {
	b := NewBreakpoints()
	s0 := b.Seq()
	b.Toggle("/w/a.go", 5)
	s1 := b.Seq()
	b.ApplyEdit("/w/a.go", document.Op{Type: document.OpInsert, InsertLine: 0, InsertText: "x\n"})
	s2 := b.Seq()
	b.ApplyEdit("/w/a.go", document.Op{Type: document.OpInsert, InsertLine: 99, InsertText: "x\n"}) // below: no change
	if s0 >= s1 || s1 >= s2 || b.Seq() != s2 {
		t.Errorf("seq %d,%d,%d,%d: must rise on a change and hold otherwise", s0, s1, s2, b.Seq())
	}
}

// A breakpoint stays with its line's text through every kind of edit.
func TestApplyEditKeepsBreakpointsWithTheirText(t *testing.T) {
	ins := func(line, col int, text string) document.Op {
		return document.Op{Type: document.OpInsert, InsertLine: line, InsertCol: col, InsertText: text}
	}
	del := func(fl, fc, tl, tc int) document.Op {
		return document.Op{Type: document.OpDelete, FromLine: fl, FromCol: fc, ToLine: tl, ToCol: tc}
	}
	for _, tc := range []struct {
		name string
		op   document.Op
		want string // breakpoints start on lines 2,5,8
	}{
		{"insert lines above", ins(0, 0, "a\nb\n"), "4,7,10"},
		{"insert at column 0 of a breakpoint line pushes it down", ins(5, 0, "new\n"), "2,6,9"},
		{"splitting a breakpoint line after column 0 keeps it", ins(5, 3, "\n"), "2,5,9"},
		{"insert below", ins(9, 0, "x\n"), "2,5,8"},
		{"insert with no newline changes nothing", ins(5, 0, "abc"), "2,5,8"},
		{"delete whole lines holding a breakpoint", del(4, 0, 6, 0), "2,6"},
		{"delete whole lines above", del(0, 0, 2, 0), "0,3,6"},
		{"join lines removes the merged ones", del(4, 7, 5, 2), "2,7"},
		{"join onto a breakpoint line keeps it", del(5, 9, 6, 0), "2,5,7"},
		{"delete within one line changes nothing", del(5, 1, 5, 4), "2,5,8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBreakpoints()
			for _, l := range []int{2, 5, 8} {
				b.Toggle("/w/a.go", l)
			}
			b.ApplyEdit("/w/a.go", tc.op)
			if got := lines(b, "/w/a.go"); got != tc.want {
				t.Errorf("breakpoints = %s, want %s", got, tc.want)
			}
		})
	}
}

// A moved breakpoint has not been confirmed by the adapter at its new line.
func TestMovedBreakpointIsUnverified(t *testing.T) {
	b := NewBreakpoints()
	b.Toggle("/w/a.go", 5)
	b.setResults("/w/a.go", nil, []int{5}, []resultLine{{verified: true, line: 5}})
	b.ApplyEdit("/w/a.go", document.Op{Type: document.OpInsert, InsertLine: 0, InsertText: "\n"})
	bps, _ := b.List("/w/a.go")
	if bps[0].Line != 6 || bps[0].Verified {
		t.Errorf("after moving: %+v, want line 6, unverified", bps[0])
	}
}

// The adapter may place a breakpoint on a different line (a blank line moves
// to the next statement); the marker follows it.
func TestSetResultsFollowsTheAdapter(t *testing.T) {
	b := NewBreakpoints()
	b.Toggle("/w/a.go", 3)
	b.Toggle("/w/a.go", 9)
	b.setResults("/w/a.go", nil, []int{3, 9}, []resultLine{
		{verified: true, line: 4},
		{verified: false, line: -1, message: "no code at this line"},
	})
	bps, _ := b.List("/w/a.go")
	if bps[0].Line != 4 || !bps[0].Verified {
		t.Errorf("first = %+v, want moved to 4, verified", bps[0])
	}
	if bps[1].Line != 9 || bps[1].Verified || bps[1].Message == "" {
		t.Errorf("second = %+v, want kept at 9, unverified with the reason", bps[1])
	}
}
