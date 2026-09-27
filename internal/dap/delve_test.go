package dap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// debuggee is a tiny program with a known line to stop on.
const debuggee = `package main

import "fmt"

func main() {
	x := 42
	names := []string{"a", "b"}
	fmt.Println(x, names) // line 8: the breakpoint
}
`

const breakpointLine = 8

// eventLog collects events and lets a test wait for one.
type eventLog struct {
	mu     sync.Mutex
	events []Event
	signal chan struct{}
}

func newEventLog() *eventLog { return &eventLog{signal: make(chan struct{}, 1)} }

func (l *eventLog) handle(e Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
	select {
	case l.signal <- struct{}{}:
	default:
	}
}

// waitFor returns the first event named name that has not been returned yet
// by an earlier waitFor, waiting up to timeout.
func (l *eventLog) waitFor(t *testing.T, name string, from *int, timeout time.Duration) Event {
	t.Helper()
	deadline := time.After(timeout)
	for {
		l.mu.Lock()
		for i := *from; i < len(l.events); i++ {
			if l.events[i].Event == name {
				*from = i + 1
				e := l.events[i]
				l.mu.Unlock()
				return e
			}
		}
		l.mu.Unlock()
		select {
		case <-l.signal:
		case <-deadline:
			l.mu.Lock()
			var seen []string
			for _, e := range l.events {
				seen = append(seen, e.Event)
			}
			l.mu.Unlock()
			t.Fatalf("no %q event within %v (saw %v)", name, timeout, seen)
		}
	}
}

// cannotDebug reports why this machine cannot launch a program under a
// debugger, or "" if it can.
//
// On macOS with Developer Mode off (DevToolsSecurity), every launch or attach
// asks for authorization in a GUI prompt. Nothing headless can answer it, so
// Delve's launch never returns — the test would spend its whole timeout and
// fail, on every run, on an ordinary developer machine. Found exactly that way.
//
// INDIGO_DEBUG_TESTS=1 skips the check, for a machine where launches are
// authorized some other way (the prompt approved, or a signed dlv).
func cannotDebug() string {
	if runtime.GOOS != "darwin" || os.Getenv("INDIGO_DEBUG_TESTS") == "1" {
		return ""
	}
	out, err := exec.Command("DevToolsSecurity", "-status").CombinedOutput()
	if err == nil && strings.Contains(string(out), "disabled") {
		return "macOS Developer Mode is disabled, so a debugger launch waits on an " +
			"authorization prompt; enable it with `sudo DevToolsSecurity -enable`"
	}
	return ""
}

// TestDelveEndToEnd drives a real `dlv dap` through a whole session: launch,
// stop at a breakpoint, read the stack, scopes and variables, evaluate, and
// continue to exit. The fake adapter tests pin the client's mechanics; this is
// what says those mechanics match a real adapter's behaviour.
func TestDelveEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and debugs a program; skipped in -short")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Skip("dlv not installed")
	}
	if reason := cannotDebug(); reason != "" {
		t.Skip(reason)
	}
	// Symlinks resolved before anything else, and the program launched from the
	// resolved directory. Delve matches a breakpoint's path against the path the
	// compiler recorded, which is the directory the build ran in, spelled as it
	// was given — so launching from /var/... and setting a breakpoint on
	// /private/var/... (macOS's TMPDIR symlink) failed with "could not find
	// file". indigo resolves symlinks in its workspace path (cmd/indigo's
	// resolvePath), so the server must launch from that same spelling.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.go")
	if err := os.WriteFile(main, []byte(debuggee), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module debuggee\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second) // includes a go build
	defer cancel()

	log := newEventLog()
	c, err := StartListening(ctx, dlv, []string{"dap", "--listen=127.0.0.1:0"}, dir, nil, log.handle)
	if err != nil {
		t.Fatalf("start dlv dap: %v", err)
	}
	defer c.Shutdown()

	caps, err := c.Initialize(ctx, "go")
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	var placed []Breakpoint
	err = c.LaunchSession(ctx, caps, map[string]any{
		"request": "launch",
		"mode":    "debug",
		"program": dir,
		// Send the program's stdout/stderr as output events. Without it
		// Delve writes them to its own stdout, which StartListening reads
		// only to keep the pipe from filling — so they never reach a client.
		"outputMode": "remote",
	}, func(ctx context.Context) error {
		var err error
		placed, err = c.SetBreakpoints(ctx, main, []SourceBreakpoint{{Line: breakpointLine}})
		return err
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(placed) != 1 || !placed[0].Verified || placed[0].Line != breakpointLine {
		t.Fatalf("breakpoint placed as %+v, want verified at line %d", placed, breakpointLine)
	}

	var cursor int
	var stopped StoppedEvent
	if err := log.waitFor(t, "stopped", &cursor, 30*time.Second).Decode(&stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.Reason != "breakpoint" {
		t.Errorf("stopped for %q, want breakpoint", stopped.Reason)
	}

	frames, err := c.StackTrace(ctx, stopped.ThreadID, 20)
	if err != nil || len(frames) == 0 {
		t.Fatalf("stackTrace = %v, %v", frames, err)
	}
	top := frames[0]
	if top.Source == nil || top.Source.Path != main || top.Line != breakpointLine || top.Name != "main.main" {
		t.Errorf("top frame = %+v (source %+v), want main.main at %s:%d", top, top.Source, main, breakpointLine)
	}

	scopes, err := c.Scopes(ctx, top.ID)
	if err != nil || len(scopes) == 0 {
		t.Fatalf("scopes = %v, %v", scopes, err)
	}
	var locals []Variable
	for _, s := range scopes {
		if strings.EqualFold(s.Name, "locals") {
			if locals, err = c.Variables(ctx, s.VariablesReference); err != nil {
				t.Fatalf("variables: %v", err)
			}
		}
	}
	vars := map[string]Variable{}
	for _, v := range locals {
		vars[v.Name] = v
	}
	if vars["x"].Value != "42" {
		t.Errorf("x = %q, want 42 (locals: %+v)", vars["x"].Value, locals)
	}
	names, ok := vars["names"]
	if !ok || names.VariablesReference == 0 {
		t.Fatalf("names missing or not expandable: %+v", names)
	}
	elems, err := c.Variables(ctx, names.VariablesReference)
	if err != nil || len(elems) != 2 || !strings.Contains(elems[1].Value, "b") {
		t.Errorf("names elements = %+v, %v", elems, err)
	}

	res, err := c.Evaluate(ctx, "x + 1", top.ID, EvalHover)
	if err != nil || res.Result != "43" {
		t.Errorf("evaluate x+1 = %+v, %v; want 43", res, err)
	}

	if err := c.Continue(ctx, stopped.ThreadID); err != nil {
		t.Fatalf("continue: %v", err)
	}
	log.waitFor(t, "terminated", &cursor, 30*time.Second)

	var sawOutput bool
	log.mu.Lock()
	for _, e := range log.events {
		var o OutputEvent
		if e.Event == "output" && e.Decode(&o) == nil && strings.Contains(o.Output, "42 [a b]") {
			sawOutput = true
		}
	}
	log.mu.Unlock()
	if !sawOutput {
		t.Error("the program's stdout did not arrive as an output event")
	}
}
