package rpcclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/debug"
	"github.com/indiejames/indigo/internal/document"
)

// dialTestServer starts a real server and returns a connected client with its
// pushes captured.
func dialTestServer(t *testing.T) (*RPC, *pushLog) {
	t.Helper()
	sock := startTestServer(t)
	r, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})
	log := &pushLog{}
	r.SetPushSender(log.send)
	return r, log
}

type pushLog struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (l *pushLog) send(m tea.Msg) {
	l.mu.Lock()
	l.msgs = append(l.msgs, m)
	l.mu.Unlock()
}

// maxDebugChanged is the largest sequence numbers pushed so far.
func (l *pushLog) maxDebugChanged() DebugChangedMsg {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out DebugChangedMsg
	for _, m := range l.msgs {
		if d, ok := m.(DebugChangedMsg); ok {
			out.StateSeq = max(out.StateSeq, d.StateSeq)
			out.OutputSeq = max(out.OutputSeq, d.OutputSeq)
			out.BreakpointsSeq = max(out.BreakpointsSeq, d.BreakpointsSeq)
		}
	}
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Breakpoints are the server's: set over RPC, listed back, pushed to windows,
// and kept on their line when a window edits the file above them.
func TestBreakpointsOverRPC(t *testing.T) {
	r, log := dialTestServer(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	path := filepath.Join(dir, "a.txt")                          // .txt: no language server
	os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o644) //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bufID, _, version, _, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if set, err := r.ToggleBreakpoint(ctx, path, 2); err != nil || !set {
		t.Fatalf("ToggleBreakpoint = %v, %v", set, err)
	}
	bps, seq, err := r.ListBreakpoints(ctx, path)
	if err != nil || len(bps) != 1 || bps[0].Line != 2 || bps[0].Path != path || seq == 0 {
		t.Fatalf("ListBreakpoints = %+v, seq %d, %v", bps, seq, err)
	}
	eventually(t, "a breakpoints push", func() bool { return log.maxDebugChanged().BreakpointsSeq >= seq })

	// A window inserts a line above the breakpoint.
	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "zero\n"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the breakpoint to follow its line", func() bool {
		bps, _, _ := r.ListBreakpoints(ctx, path)
		return len(bps) == 1 && bps[0].Line == 3
	})
	all, _, _ := r.ListBreakpoints(ctx, "")
	if len(all) != 1 {
		t.Errorf("ListBreakpoints(\"\") = %+v, want the one breakpoint", all)
	}
}

// A failure the user should read — here, no debugger installed — comes back as
// an ordinary error with the fix in it, not as a broken connection.
func TestDebugStartReportsAMissingDebugger(t *testing.T) {
	r, _ := dialTestServer(t)
	dir := t.TempDir()
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := r.DebugStart(ctx, DebugConfig{Adapter: "go", Program: dir})
	if err == nil || !strings.Contains(err.Error(), "go install github.com/go-delve/delve") {
		t.Fatalf("err = %v, want the missing-dlv explanation", err)
	}
	st, err := r.DebugState(ctx)
	if err != nil || st.Status != DebugTerminated || st.Error == "" {
		t.Errorf("state = %+v, %v; want terminated with the error", st, err)
	}
	if err := r.DebugControl(ctx, DebugContinue); err == nil {
		t.Error("control with no session succeeded")
	}
}

const rpcDebuggee = `package main

import "fmt"

func main() {
	x := 42
	fmt.Println("answer", x) // line 7 (0-based 6)
}
`

// The whole session over RPC against real Delve: a breakpoint set before
// starting, the stop reported in DebugState and pushed, the stack, variables
// and evaluation, then continue to exit with the output captured.
func TestDebugSessionOverRPC(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and debugs a program")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := debug.PermissionHint(); hint != "" {
			t.Skip(hint + " (or set INDIGO_DEBUG_TESTS=1)")
		}
	}
	r, log := dialTestServer(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte(rpcDebuggee), 0o644)                                     //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if _, err := r.ToggleBreakpoint(ctx, main, 6); err != nil {
		t.Fatal(err)
	}
	// Cwd given: the server otherwise builds from its workspace directory,
	// which here is a different temp dir from the program's module.
	if err := r.DebugStart(ctx, DebugConfig{Adapter: "go", Program: dir, Cwd: dir}); err != nil {
		if strings.Contains(err.Error(), "(dlv) was not found") {
			t.Skip("dlv not installed")
		}
		t.Fatalf("DebugStart: %v", err)
	}
	var st DebugState
	eventually(t, "the session to stop", func() bool {
		st, _ = r.DebugState(ctx)
		return st.Status == DebugStopped
	})
	if st.Path != main || st.Line != 6 {
		t.Errorf("stopped at %s:%d, want %s:6", st.Path, st.Line, main)
	}
	eventually(t, "the stop to be pushed", func() bool { return log.maxDebugChanged().StateSeq >= st.Seq })
	bps, _, _ := r.ListBreakpoints(ctx, main)
	if len(bps) != 1 || !bps[0].Verified {
		t.Errorf("breakpoint = %+v, want verified", bps)
	}

	frames, err := r.DebugStackTrace(ctx, 0)
	if err != nil || len(frames) == 0 || frames[0].Line != 6 {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	scopes, err := r.DebugScopes(ctx, frames[0].ID)
	if err != nil || len(scopes) == 0 {
		t.Fatalf("scopes = %+v, %v", scopes, err)
	}
	vars, err := r.DebugVariables(ctx, scopes[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range vars {
		found = found || (v.Name == "x" && v.Value == "42")
	}
	if !found {
		t.Errorf("x=42 not among %+v", vars)
	}
	if v, err := r.DebugEvaluate(ctx, "x + 1", frames[0].ID, "hover"); err != nil || v.Value != "43" {
		t.Errorf("evaluate = %+v, %v", v, err)
	}

	if err := r.DebugControl(ctx, DebugContinue); err != nil {
		t.Fatalf("continue: %v", err)
	}
	eventually(t, "the program to finish", func() bool {
		st, _ = r.DebugState(ctx)
		return st.Status == DebugTerminated
	})
	chunks, latest, _, err := r.DebugOutput(ctx, 0)
	if err != nil || latest == 0 {
		t.Fatalf("output = %+v, latest %d, %v", chunks, latest, err)
	}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(c.Text)
	}
	if !strings.Contains(out.String(), "answer 42") {
		t.Errorf("program output %q lacks its line", out.String())
	}
}
