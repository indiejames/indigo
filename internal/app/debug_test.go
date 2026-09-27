package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func debugApp(t *testing.T, paths ...string) App {
	t.Helper()
	a := App{cfg: &config.Config{LineNumbers: true}, width: 80, height: 24, rpc: &client.RPC{}}
	for i, p := range paths {
		a.buffers = append(a.buffers, client.New(&client.RPC{}, uint32(i+1), "a\nb\nc\nd\n", 0, p, "/w", &config.Config{LineNumbers: true}, false, 0))
	}
	a.resizeAllBuffers()
	return a
}

func activeRows(a App) []string {
	return strings.Split(ansi.Strip(a.buffers[a.active].View().Content), "\n")
}

// Breakpoints fetched from the server appear in the gutter of the buffer they
// belong to, and not in another.
func TestBreakpointsReachTheActiveBuffer(t *testing.T) {
	a := debugApp(t, "/w/a.go", "/w/b.go")
	a = a.handleDebugBreakpoints(debugBreakpointsMsg{
		breakpoints: []client.DebugBreakpoint{{Path: "/w/a.go", Line: 2}},
		seq:         1,
	})
	updated, _ := a.Update(nil) // any message; the epilogue applies the view
	a = updated.(App)
	if row := activeRows(a)[2]; !strings.HasPrefix(row, "●") {
		t.Errorf("a.go row 2 = %q, want a breakpoint", row)
	}
	a.active = 1
	updated, _ = a.Update(nil)
	a = updated.(App)
	for _, row := range activeRows(a) {
		if strings.HasPrefix(row, "●") {
			t.Errorf("b.go shows a.go's breakpoint: %q", row)
		}
	}
}

// When the session stops, the window the user was last in jumps there; any
// other window only shows the arrow if it has the file open.
func TestStopJumpsOnlyTheActiveWindow(t *testing.T) {
	stopped := client.DebugState{Status: client.DebugStopped, Seq: 5, Path: "/w/b.go", Line: 3, Reason: "breakpoint"}

	// This window (client id 0 for a bare RPC) is the active one: it jumps.
	a := debugApp(t, "/w/a.go", "/w/b.go")
	_, cmd := a.handleDebugState(debugStateMsg{state: stopped, activeClient: 0})
	if cmd == nil {
		t.Fatal("the active window did not jump to the stop")
	}
	sw, ok := cmd().(switchBufferMsg)
	if !ok || sw.idx != 1 || sw.line != 3 {
		t.Errorf("jump = %+v, want switch to b.go line 3", sw)
	}

	// Another window is active: no jump, but b.go shows where execution is.
	b := debugApp(t, "/w/a.go", "/w/b.go")
	b, cmd = b.handleDebugState(debugStateMsg{state: stopped, activeClient: 99})
	if cmd != nil {
		t.Error("a window that is not the active one jumped")
	}
	b.active = 1
	updated, _ := b.Update(nil)
	b = updated.(App)
	if row := activeRows(b)[3]; !strings.HasPrefix(row, "▶") {
		t.Errorf("b.go row 3 = %q, want the stopped-here arrow", row)
	}
}

// Fetches can return out of order; an older state must not overwrite a newer.
func TestOlderDebugStateIsIgnored(t *testing.T) {
	a := debugApp(t, "/w/a.go")
	a, _ = a.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugStopped, Seq: 7, Path: "/w/a.go"}, activeClient: 99})
	a, _ = a.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugRunning, Seq: 6}, activeClient: 99})
	if a.debug.state.Status != client.DebugStopped {
		t.Errorf("status = %v, want the newer stopped state kept", a.debug.state.Status)
	}
}

// The end of a session is reported, with its exit code or its error.
func TestSessionEndIsReported(t *testing.T) {
	a := debugApp(t, "/w/a.go")
	a, _ = a.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugRunning, Seq: 1}})
	a, _ = a.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugTerminated, Seq: 2, HasExitCode: true, ExitCode: 3}})
	if !strings.Contains(a.status, "exit code 3") {
		t.Errorf("status = %q, want the exit code", a.status)
	}
	b := debugApp(t, "/w/a.go")
	b, _ = b.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugStarting, Seq: 1}})
	b, _ = b.handleDebugState(debugStateMsg{state: client.DebugState{Status: client.DebugTerminated, Seq: 2, Error: "build failed"}})
	if !strings.Contains(b.status, "build failed") {
		t.Errorf("status = %q, want the error", b.status)
	}
}

// A push only triggers a fetch when a sequence number moved past what the
// window already has.
func TestDebugChangedFetchesOnlyWhatMoved(t *testing.T) {
	a := debugApp(t, "/w/a.go")
	a.debug = newAppDebug()
	a.debug.stateSeen, a.debug.bpSeen = 5, 5
	if _, cmd := a.handleDebugChanged(client.DebugChangedMsg{StateSeq: 4, BreakpointsSeq: 5}); cmd != nil {
		t.Error("an old push triggered a fetch")
	}
	if _, cmd := a.handleDebugChanged(client.DebugChangedMsg{StateSeq: 6, BreakpointsSeq: 5}); cmd == nil {
		t.Error("a newer state seq did not trigger a fetch")
	}
}
