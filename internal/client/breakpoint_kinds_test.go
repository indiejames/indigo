package client

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/document"
)

// The gutter says what kind of breakpoint a line has, hollow when the running
// session could not set it.
func TestBreakpointKindGlyphs(t *testing.T) {
	m := debugTestModel("a\nb\nc\n")
	m = m.WithDebugView(DebugView{Gen: 1, Breakpoints: map[int]BreakpointMark{
		0: {Verified: true},
		1: {Verified: true, Condition: "i > 2"},
		2: {Verified: true, LogMessage: "i={i}"},
	}})
	for line, want := range map[int]string{0: "●", 1: "◉", 2: "◆"} {
		if got := ansi.Strip(m.debugGutterMark(line)); !strings.HasPrefix(got, want) {
			t.Errorf("line %d: %q, want %s", line, got, want)
		}
	}
	m = m.WithDebugView(DebugView{Gen: 2, Status: DebugRunning, Breakpoints: map[int]BreakpointMark{
		0: {}, 1: {Condition: "i > 2"}, 2: {LogMessage: "i={i}"},
	}})
	for line, want := range map[int]string{0: "○", 1: "◎", 2: "◇"} {
		if got := ansi.Strip(m.debugGutterMark(line)); !strings.HasPrefix(got, want) {
			t.Errorf("unverified line %d: %q, want %s", line, got, want)
		}
	}
}

// The condition or log message is shown after its line — otherwise nothing
// on screen says a breakpoint is conditional — and so is the reason a
// session could not set one.
func TestBreakpointNotesAreShownAfterTheLine(t *testing.T) {
	m := debugTestModel("total += i\nfmt.Println(i)\nplain()\n")
	m = m.WithDebugView(DebugView{Gen: 1, Status: DebugRunning, Breakpoints: map[int]BreakpointMark{
		0: {Verified: true, LogMessage: "i={i}"},
		1: {Condition: "i == 3", Detail: "this debugger does not support conditional breakpoints"},
		2: {Verified: true},
	}})
	rows := viewLines(m)
	find := func(text string) string {
		for _, r := range rows {
			if strings.Contains(r, text) {
				return r
			}
		}
		return ""
	}
	if r := find("total += i"); !strings.Contains(r, "log: i={i}") {
		t.Errorf("logpoint row = %q", r)
	}
	if r := find("fmt.Println(i)"); !strings.Contains(r, "if i == 3 — this debugger does not support") {
		t.Errorf("conditional row = %q", r)
	}
	if r := find("plain()"); strings.TrimRight(r, " ") != strings.TrimRight(r[:strings.Index(r, "plain()")+len("plain()")], " ") {
		t.Errorf("a plain breakpoint should have no note: %q", r)
	}
}

// A long note is cut to the window rather than running off the edge.
func TestLongBreakpointNoteIsCut(t *testing.T) {
	m := debugTestModel("x\n")
	m = m.WithDebugView(DebugView{Gen: 1, Breakpoints: map[int]BreakpointMark{
		0: {Verified: true, Condition: strings.Repeat("a", 500)},
	}})
	for _, r := range viewLines(m) {
		if w := ansi.StringWidth(r); w > m.width {
			t.Fatalf("row is %d wide in a %d-wide window: %q", w, m.width, r)
		}
		if strings.Contains(r, "if aaa") && !strings.Contains(r, "…") {
			t.Errorf("cut note has no ellipsis: %q", r)
		}
	}
}

// The prompt keys ask the App for a prompt pre-filled with the field being
// edited, carrying the other one through so it is not cleared.
func TestBreakpointPromptCarriesTheOtherField(t *testing.T) {
	m := newTestModel("a\nb\n")
	m.filePath = "/w/a.go"
	m.cursor = document.Pos{Line: 1}
	m = m.WithDebugView(DebugView{Gen: 1, Breakpoints: map[int]BreakpointMark{1: {Condition: "c", LogMessage: "l"}}})

	_, cmd := executeBreakpointCondition(m)
	if got := cmd().(BreakpointPromptMsg); got != (BreakpointPromptMsg{Path: "/w/a.go", Line: 1, Current: "c", Keep: "l"}) {
		t.Errorf("condition prompt = %+v", got)
	}
	_, cmd = executeLogpoint(m)
	if got := cmd().(BreakpointPromptMsg); got != (BreakpointPromptMsg{Path: "/w/a.go", Line: 1, Log: true, Current: "l", Keep: "c"}) {
		t.Errorf("logpoint prompt = %+v", got)
	}
	m.filePath = ""
	if _, cmd := executeLogpoint(m); cmd != nil {
		t.Error("an unsaved buffer should not prompt")
	}
}
