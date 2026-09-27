package debug

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/dap"
)

const debuggee = `package main

import "fmt"

func main() {
	x := 42
	names := []string{"a", "b"}
	fmt.Println(x, names) // line 8 (0-based 7): the breakpoint
}
`

// notifications records every notify call, for asserting that windows would
// have been told about each change.
type notifications struct {
	mu    sync.Mutex
	calls [][3]uint64
}

func (n *notifications) notify(s, o, b uint64) {
	n.mu.Lock()
	n.calls = append(n.calls, [3]uint64{s, o, b})
	n.mu.Unlock()
}

func (n *notifications) last() [3]uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.calls) == 0 {
		return [3]uint64{}
	}
	return n.calls[len(n.calls)-1]
}

func waitState(t *testing.T, m *Manager, want Status, timeout time.Duration) State {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		st, _ := m.State()
		if st.Status == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %+v, want %v", st, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func canDebug(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and debugs a program; skipped in -short")
	}
	if _, err := FindDelve(); err != nil {
		t.Skip("dlv not installed")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := PermissionHint(); hint != "" {
			t.Skip(hint + " (or set INDIGO_DEBUG_TESTS=1)")
		}
	}
}

// TestManagerDebugsAGoProgram is the server-side session end to end against
// real Delve: a breakpoint set before starting is sent and verified, the
// session stops there and reports the file and line, the stack and variables
// can be read, and continuing runs the program to exit with its output
// captured. Windows are notified at each step.
func TestManagerDebugsAGoProgram(t *testing.T) {
	canDebug(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte(debuggee), 0o644)                                        //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck

	var n notifications
	m := NewManager(n.notify)
	t.Cleanup(m.Shutdown)

	if !m.ToggleBreakpoint(main, 7) {
		t.Fatal("breakpoint not set")
	}
	if err := m.Start(Config{Adapter: "go", Program: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	st := waitState(t, m, StatusStopped, 30*time.Second)
	if st.Path != main || st.Line != 7 || st.Reason != "breakpoint" {
		t.Errorf("stopped at %s:%d (%s), want %s:7 breakpoint", st.Path, st.Line, st.Reason, main)
	}
	bps, _ := m.Breakpoints.List(main)
	if len(bps) != 1 || !bps[0].Verified || bps[0].Line != 7 {
		t.Errorf("breakpoint after start = %+v, want verified at 7", bps)
	}

	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 || frames[0].Line != 7 || frames[0].Name != "main.main" {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	scopes, err := m.Scopes(frames[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var x string
	for _, s := range scopes {
		if strings.EqualFold(s.Name, "locals") {
			vars, _ := m.Variables(s.Ref)
			for _, v := range vars {
				if v.Name == "x" {
					x = v.Value
				}
			}
		}
	}
	if x != "42" {
		t.Errorf("x = %q, want 42", x)
	}
	if v, err := m.Evaluate("x * 2", frames[0].ID, dap.EvalHover); err != nil || v.Value != "84" {
		t.Errorf("evaluate = %+v, %v; want 84", v, err)
	}

	if err := m.Control(ActionContinue); err != nil {
		t.Fatalf("continue: %v", err)
	}
	end := waitState(t, m, StatusTerminated, 30*time.Second)
	if end.Error != "" {
		t.Errorf("ended with an error: %+v", end)
	}
	// Delve reports the exit status only in a console message after its first
	// "terminated", so it can land a moment after the state first reads
	// terminated.
	deadline := time.Now().Add(5 * time.Second)
	for !end.HasExitCode && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		end, _ = m.State()
	}
	if !end.HasExitCode || end.ExitCode != 0 {
		t.Errorf("ended as %+v, want exit code 0 recorded", end)
	}
	chunks, _, _ := m.Output(0)
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(c.Text)
	}
	if !strings.Contains(out.String(), "42 [a b]") {
		t.Errorf("program output not captured: %q", out.String())
	}
	if last := n.last(); last[0] == 0 || last[1] == 0 || last[2] == 0 {
		t.Errorf("last notification %v: state, output and breakpoints should all have moved", last)
	}
	if _, err := m.StackTrace(0); !errors.Is(err, ErrNoSession) {
		t.Errorf("after the session ended, StackTrace err = %v, want ErrNoSession", err)
	}
}

// silentAdapter accepts a connection and never answers — a launch stuck
// behind an authorization prompt looks exactly like this from the client.
func silentAdapter(_ context.Context, _ Config, handler func(dap.Event)) (*dap.Client, error) {
	clientEnd, adapterEnd := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := adapterEnd.Read(buf); err != nil {
				return
			}
		}
	}()
	return dap.NewClient(clientEnd, handler), nil
}

// A launch that never completes ends with an error that says what happened —
// and, on a Mac with Developer Mode off, what to do — rather than leaving the
// session "starting" forever.
func TestStartTimesOutWithAnExplanation(t *testing.T) {
	m := NewManager(nil)
	m.startAdapter = silentAdapter
	m.launchTimeout = 200 * time.Millisecond
	err := m.Start(Config{Adapter: "go", Program: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "did not start the program within") {
		t.Fatalf("err = %v, want a launch timeout", err)
	}
	if hint := PermissionHint(); hint != "" && !strings.Contains(err.Error(), "DevToolsSecurity") {
		t.Errorf("timeout on a Mac with Developer Mode off does not name the fix: %v", err)
	}
	st, _ := m.State()
	if st.Status != StatusTerminated || st.Error == "" {
		t.Errorf("state after a failed start = %+v, want terminated with the error", st)
	}
	// A failed start must not leave a session behind that blocks the next.
	m.startAdapter = silentAdapter
	if err := m.Start(Config{Adapter: "go"}); errors.Is(err, ErrSessionActive) {
		t.Error("a failed start left the manager thinking a session is running")
	}
}

func TestControlWithoutASession(t *testing.T) {
	m := NewManager(nil)
	if err := m.Control(ActionContinue); !errors.Is(err, ErrNoSession) {
		t.Errorf("err = %v, want ErrNoSession", err)
	}
}

func TestUnknownAdapterIsRefused(t *testing.T) {
	m := NewManager(nil)
	if err := m.Start(Config{Adapter: "cobol"}); err == nil || !strings.Contains(err.Error(), "cobol") {
		t.Errorf("err = %v, want an unsupported-adapter error naming it", err)
	}
}

// Output is kept in arrival order, fetched incrementally, and bounded — with
// a flag when a window asked for chunks that have already been dropped.
func TestOutputRetention(t *testing.T) {
	m := NewManager(nil)
	m.appendOutput("stdout", "one\n")
	m.appendOutput("stderr", "two\n")
	chunks, latest, truncated := m.Output(0)
	if len(chunks) != 2 || latest != 2 || truncated || chunks[1].Category != "stderr" {
		t.Fatalf("Output(0) = %+v, %d, %v", chunks, latest, truncated)
	}
	if chunks, _, _ := m.Output(1); len(chunks) != 1 || chunks[0].Text != "two\n" {
		t.Errorf("Output(1) = %+v, want only the second chunk", chunks)
	}
	big := strings.Repeat("x", maxOutputBytes/2+1)
	m.appendOutput("stdout", big)
	m.appendOutput("stdout", big)
	if _, _, truncated := m.Output(0); !truncated {
		t.Error("dropped chunks were not reported as truncated")
	}
}

// FindDelve must look where `go install` puts dlv, since a server started
// from a GUI launcher often lacks it on PATH.
func TestFindDelveOffPath(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "go", "bin")
	os.MkdirAll(bin, 0o755)                                               //nolint:errcheck
	os.WriteFile(filepath.Join(bin, "dlv"), []byte("#!/bin/sh\n"), 0o755) //nolint:errcheck
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("HOME", home)
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "")
	got, err := FindDelve()
	if err != nil || got != filepath.Join(bin, "dlv") {
		t.Errorf("FindDelve() = %q, %v; want ~/go/bin/dlv", got, err)
	}
}

// Delve reports the exit status only on its console; a non-zero code must come
// through, and a session that never reports one must not claim 0.
func TestExitStatusFromDelveConsole(t *testing.T) {
	m := NewManager(nil)
	m.gen = 1
	m.state = State{Status: StatusTerminated}
	m.onEvent(1, nil, dap.Event{Event: "output", Body: []byte(`{"category":"console","output":"Process 7 has exited with status 3\n"}`)})
	if st, _ := m.State(); !st.HasExitCode || st.ExitCode != 3 {
		t.Errorf("state = %+v, want exit code 3", st)
	}
	m2 := NewManager(nil)
	m2.gen = 1
	m2.onEvent(1, nil, dap.Event{Event: "output", Body: []byte(`{"category":"stdout","output":"has exited with status 9\n"}`)})
	if st, _ := m2.State(); st.HasExitCode {
		t.Error("program stdout that merely looks like the message was taken as the exit status")
	}
	// A previous session's late output is dropped.
	m2.gen = 2
	m2.onEvent(1, nil, dap.Event{Event: "output", Body: []byte(`{"category":"stdout","output":"stale\n"}`)})
	if chunks, _, _ := m2.Output(0); len(chunks) != 1 {
		t.Errorf("output = %+v, want the stale session's line dropped", chunks)
	}
}

// A launch that fails to build must say why. Delve answers "Build error: Check
// the debug console for details" and sends the compiler's errors as console
// output first; a caller with no console to check needs them in the error.
func TestStartCarriesTheBuildError(t *testing.T) {
	canDebug(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	broken := "package main\n\nfunc main() {\n\tprintln(undefinedName)\n}\n"
	os.WriteFile(filepath.Join(dir, "main.go"), []byte(broken), 0o644)                 //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	err = m.Start(Config{Adapter: "go", Program: dir, Cwd: dir})
	if err == nil || !strings.Contains(err.Error(), "undefinedName") {
		t.Fatalf("err = %v, want the compiler's message naming undefinedName", err)
	}
}
