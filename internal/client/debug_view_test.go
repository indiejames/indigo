package client

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

func debugTestModel(content string) Model {
	m := newTestModel(content)
	m.cfg = &config.Config{LineNumbers: true}
	return m
}

// viewLines is the rendered buffer, colour stripped, one entry per row.
func viewLines(m Model) []string {
	return strings.Split(ansi.Strip(m.View().Content), "\n")
}

func TestDebugGutterMarkers(t *testing.T) {
	m := debugTestModel("a\nb\nc\nd\n").WithDebugView(DebugView{
		Gen:         1,
		Breakpoints: map[int]BreakpointMark{0: {Verified: true}, 2: {}},
		Status:      DebugStopped,
		StopLine:    2,
		HasStop:     true,
	})
	rows := viewLines(m)
	if !strings.HasPrefix(rows[0], "●") {
		t.Errorf("row 0 = %q, want a breakpoint marker", rows[0])
	}
	// The stopped-here arrow outranks the breakpoint on the same line.
	if !strings.HasPrefix(rows[2], "▶") {
		t.Errorf("row 2 = %q, want the stopped-here arrow", rows[2])
	}
	if strings.HasPrefix(rows[1], "●") || strings.HasPrefix(rows[1], "▶") {
		t.Errorf("row 1 = %q, want no debug marker", rows[1])
	}
}

// A breakpoint the running session could not set is drawn hollow; with no
// session, every breakpoint is just a breakpoint.
func TestUnverifiedBreakpointIsHollowOnlyDuringASession(t *testing.T) {
	bps := map[int]BreakpointMark{0: {}}
	running := debugTestModel("a\n").WithDebugView(DebugView{Gen: 1, Breakpoints: bps, Status: DebugRunning})
	if row := viewLines(running)[0]; !strings.HasPrefix(row, "○") {
		t.Errorf("during a session: %q, want hollow ○", row)
	}
	idle := debugTestModel("a\n").WithDebugView(DebugView{Gen: 1, Breakpoints: bps})
	if row := viewLines(idle)[0]; !strings.HasPrefix(row, "●") {
		t.Errorf("no session: %q, want ●", row)
	}
}

// A Model never given a debug view draws nothing for the debugger — in
// particular no stopped-here arrow on line 1, which a -1 sentinel for "not
// stopped" would have produced from the zero value.
func TestNoDebugViewDrawsNothing(t *testing.T) {
	out := ansi.Strip(debugTestModel("a\nb\n").View().Content)
	if strings.ContainsAny(out, "▶●○") {
		t.Errorf("a buffer with no debug view shows a debug marker:\n%s", out)
	}
}

// The stopped line is tinted across the whole row, as a separate layer from
// plugin tints (which are rebuilt from decorations on every fetch).
func TestStoppedLineIsTinted(t *testing.T) {
	m := debugTestModel("a\nb\n").WithDebugView(DebugView{Gen: 1, Status: DebugStopped, StopLine: 1, HasStop: true})
	tints := m.lineTintsFor(1)
	if len(tints) == 0 || tints[0].BG != debugStopLineBG || !tints[0].ToEOL {
		t.Errorf("tints on the stopped line = %+v", tints)
	}
	if len(m.lineTintsFor(0)) != 0 {
		t.Error("a line that is not stopped on is tinted")
	}
	// A plugin tint on the same line still shows, over the stop tint.
	m.lineTints = map[int][]tintRange{1: {{StartCol: 0, EndCol: 1, BG: bgSGR("#224422")}}}
	if got := m.lineTintsFor(1); len(got) != 2 || got[0].BG != debugStopLineBG {
		t.Errorf("stop tint plus a plugin tint = %+v", got)
	}
}

// With line numbers off the left gutter only appears when it has something to
// show; breakpoints must count, or they would be invisible.
func TestBreakpointsShowTheGutterWithLineNumbersOff(t *testing.T) {
	m := newTestModel("a\nb\n")
	m.cfg = &config.Config{LineNumbers: false}
	if m.gutterWidth() != 0 {
		t.Fatalf("baseline gutter width = %d, want 0", m.gutterWidth())
	}
	m = m.WithDebugView(DebugView{Gen: 1, Breakpoints: map[int]BreakpointMark{1: {Verified: true}}})
	if m.gutterWidth() == 0 {
		t.Error("breakpoints did not make the gutter appear")
	}
}

func TestDebugStatusBadge(t *testing.T) {
	for status, want := range map[DebugStatus]string{
		DebugStarting: "DEBUG starting", DebugRunning: "DEBUG running", DebugStopped: "DEBUG breakpoint",
	} {
		m := debugTestModel("a\n").WithDebugView(DebugView{Gen: 1, Status: status, Reason: "breakpoint"})
		if bar := ansi.Strip(m.renderStatusBar()); !strings.Contains(bar, want) {
			t.Errorf("%v: status bar %q lacks %q", status, bar, want)
		}
	}
	if bar := ansi.Strip(debugTestModel("a\n").renderStatusBar()); strings.Contains(bar, "DEBUG") {
		t.Errorf("no session, but the status bar says %q", bar)
	}
}

func TestExpressionAtCursor(t *testing.T) {
	for _, tc := range []struct {
		line string
		col  int
		want string
	}{
		{"\treturn cfg.Name", 12, "cfg.Name"}, // on Name: the selector chain comes too
		{"\treturn cfg.Name", 9, "cfg"},       // on cfg: only what is left of the cursor's word end
		{"x := 42", 0, "x"},
		{"x := 42", 2, ""}, // on ":" — nothing to evaluate
	} {
		m := newTestModel(tc.line + "\n")
		m.cursor = document.Pos{Line: 0, Col: tc.col}
		if got := m.expressionAtCursor(); got != tc.want {
			t.Errorf("%q at %d: %q, want %q", tc.line, tc.col, got, tc.want)
		}
	}
	// A one-line selection is evaluated as selected.
	m := newTestModel("a + b\n")
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 4}}
	if got := m.expressionAtCursor(); got != "a + b" {
		t.Errorf("selection: %q, want %q", got, "a + b")
	}
}

func TestDebugConfigFor(t *testing.T) {
	m := newTestModel("")
	m.filePath = "/w/cmd/tool/main.go"
	if cfg, err := m.debugConfigFor(); err != nil || cfg.Mode != "debug" || cfg.Program != "/w/cmd/tool" {
		t.Errorf("main.go: %+v, %v", cfg, err)
	}
	m.filePath = "/w/pkg/x_test.go"
	if cfg, err := m.debugConfigFor(); err != nil || cfg.Mode != "test" {
		t.Errorf("a test file should debug in test mode: %+v, %v", cfg, err)
	}
	m.filePath = "/w/app.py"
	if cfg, err := m.debugConfigFor(); err != nil || cfg.Program != "/w/app.py" || cfg.Adapter != "" {
		t.Errorf("another language debugs the file, adapter chosen by the server: %+v, %v", cfg, err)
	}
	m.filePath = ""
	if _, err := m.debugConfigFor(); err == nil {
		t.Error("an unsaved buffer has nothing to debug")
	}
}

// The debug keys are reachable both as function keys and under Space d, and
// survive a keybinding override rebuilding the tables from their defaults.
func TestDebugKeysAreBound(t *testing.T) {
	for _, seq := range [][]string{{"f5"}, {"f9"}, {"f10"}, {"f11"}, {"shift+f11"}, {"shift+f5"}, {"space", "d", "b"}, {"space", "d", "B"}, {"space", "d", "L"}, {"space", "d", "d"}, {"space", "d", "t"}, {"space", "d", "l"}, {"space", "d", "r"}} {
		if _, ok := findIn(prefixCmds, seq); !ok {
			t.Errorf("%v is not bound", seq)
		}
		if _, ok := findIn(defaultPrefixCmds, seq); !ok {
			t.Errorf("%v is missing from the defaults a keybinding override rebuilds from", seq)
		}
	}
}

// Stepping with no session says how to start one rather than calling the server.
func TestDebugControlWithoutASession(t *testing.T) {
	m := newTestModel("a\n")
	updated, cmd := executeDebugNext(m)
	if cmd != nil {
		t.Error("stepping with no session issued a command")
	}
	if !strings.Contains(updated.(Model).status, "No debug session") {
		t.Errorf("status = %q", updated.(Model).status)
	}
}
