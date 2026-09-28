package debug

import (
	"fmt"
	"testing"

	"github.com/indiejames/indigo/internal/dap"
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

// A session is a tree of connections and each child is a separate program, so
// a breakpoint one connection could not set is still set if another could —
// and which answer arrives last must not decide it. The js-debug shape: the
// root debugs nothing and answers "not verified" for everything, while the
// child that runs the program verifies. Both orders, because the two are sent
// from different goroutines at session start and this was a race.
func TestVerifiedByAnyConnectionWins(t *testing.T) {
	root, child := &dap.Client{}, &dap.Client{}
	for _, tc := range []struct {
		name  string
		first *dap.Client
		last  *dap.Client
	}{
		{"root answers last", child, root},
		{"child answers last", root, child},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := func(b *Breakpoints, c *dap.Client) {
				b.setResults("/w/prog.ts", c, []int{2}, []resultLine{
					{id: 1, verified: c == child, line: -1},
				})
			}
			b := NewBreakpoints()
			b.Toggle("/w/prog.ts", 2)
			answer(b, tc.first)
			answer(b, tc.last)
			bps, _ := b.List("/w/prog.ts")
			if len(bps) != 1 || !bps[0].Verified {
				t.Errorf("after %s: %+v, want verified — the child set it", tc.name, bps)
			}
		})
	}
}

// The reason a breakpoint could not be set survives while no connection has
// set it, and is the root's — the connection a locally-generated message
// ("this debugger does not support logpoints") comes from.
func TestUnverifiedKeepsTheFirstReason(t *testing.T) {
	root, child := &dap.Client{}, &dap.Client{}
	b := NewBreakpoints()
	b.Toggle("/w/prog.ts", 2)
	b.setResults("/w/prog.ts", root, []int{2}, []resultLine{{line: -1, message: "no code at this line"}})
	b.setResults("/w/prog.ts", child, []int{2}, []resultLine{{id: 1, line: -1, message: "not loaded yet"}})
	bps, _ := b.List("/w/prog.ts")
	if bps[0].Verified || bps[0].Message != "no code at this line" {
		t.Errorf("got %+v, want unverified with the root's reason", bps[0])
	}
}

// A child's "verified" must not outlive the child: its program is gone, so
// the breakpoint is no longer set in anything.
func TestForgetConnectionDropsADepartedChildsAnswer(t *testing.T) {
	root, child := &dap.Client{}, &dap.Client{}
	b := NewBreakpoints()
	b.Toggle("/w/prog.ts", 2)
	b.setResults("/w/prog.ts", root, []int{2}, []resultLine{{line: -1}})
	b.setResults("/w/prog.ts", child, []int{2}, []resultLine{{id: 1, verified: true, line: -1}})
	if bps, _ := b.List("/w/prog.ts"); !bps[0].Verified {
		t.Fatalf("before the child left: %+v, want verified", bps[0])
	}
	before := b.Seq()
	b.ForgetConnection(child)
	bps, _ := b.List("/w/prog.ts")
	if bps[0].Verified {
		t.Errorf("after the child left: %+v, want unverified", bps[0])
	}
	if b.Seq() == before {
		t.Error("seq did not move, so no window refetches the change")
	}
}

// Changing a breakpoint's condition discards what connections said about the
// old one: otherwise the next answer from any connection recomputes a
// "verified" that was given for a different breakpoint.
func TestChangingAConditionDropsOldAnswers(t *testing.T) {
	root, child := &dap.Client{}, &dap.Client{}
	b := NewBreakpoints()
	b.Toggle("/w/prog.ts", 2)
	b.setResults("/w/prog.ts", child, []int{2}, []resultLine{{id: 1, verified: true, line: -1}})
	b.Set("/w/prog.ts", 2, "i > 10", "")
	// The root has re-answered about the new condition; the child has not.
	b.setResults("/w/prog.ts", root, []int{2}, []resultLine{{line: -1}})
	bps, _ := b.List("/w/prog.ts")
	if bps[0].Verified {
		t.Errorf("got %+v, want unverified until a connection confirms the condition", bps[0])
	}
}
