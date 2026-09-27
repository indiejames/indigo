package debug

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
)

const tsDebuggee = `function add(a: number, b: number): number {
	const sum: number = a + b;
	return sum; // line 3 (0-based 2)
}

const total: number = add(2, 40);
console.log("total", total);
process.exitCode = 3;
`

// jsDebugAdapter returns the built-in node adapter, pointed at js-debug's
// server script when INDIGO_JS_DEBUG names one, or skips.
func jsDebugAdapter(t *testing.T) []config.DebugAdapter {
	t.Helper()
	if testing.Short() {
		t.Skip("debugs a program")
	}
	adapters := append([]config.DebugAdapter(nil), config.DefaultDebugAdapters...)
	for i := range adapters {
		if adapters[i].Name != "node" {
			continue
		}
		if script := os.Getenv("INDIGO_JS_DEBUG"); script != "" {
			adapters[i].Command = "node"
			adapters[i].Args = append([]string{script}, adapters[i].Args...)
		} else if _, err := findCommand(adapters[i].Command); err != nil {
			if _, _, err := findJSDebug(nil); err != nil {
				t.Skip("js-debug not installed (set INDIGO_JS_DEBUG to its dapDebugServer.js)")
			}
		}
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	return adapters
}

// TypeScript through js-debug, end to end: the root session asks for a child
// with startDebugging, the breakpoint reaches the child and stops the program
// in the .ts file, evaluation and continuing go to the child, the program's
// output arrives, and the child ending does not end the session early — the
// root's terminated does. Relies on Node's own type stripping (Node 23.6+,
// or 22.6+ with --experimental-strip-types).
func TestTypeScriptThroughJSDebug(t *testing.T) {
	adapters := jsDebugAdapter(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "prog.ts")
	os.WriteFile(file, []byte(tsDebuggee), 0o644) //nolint:errcheck

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetAdapters(adapters)
	m.ToggleBreakpoint(file, 2)
	if err := m.Start(Config{Program: file, Cwd: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Path != file || st.Line != 2 {
		chunks, _, _ := m.Output(0)
		t.Fatalf("stopped at %s:%d, want %s:2\noutput %+v", st.Path, st.Line, file, chunks)
	}
	m.mu.Lock()
	children := len(m.children)
	m.mu.Unlock()
	if children == 0 {
		t.Error("no child session: the stop should have come through one")
	}
	bps, _ := m.Breakpoints.List(file)
	if len(bps) != 1 || !bps[0].Verified {
		t.Errorf("breakpoint = %+v, want verified", bps)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 || !strings.HasSuffix(frames[0].Name, "add") {
		t.Fatalf("stack = %+v, %v; want add on top", frames, err)
	}
	if v, err := m.Evaluate("a + b", frames[0].ID, dap.EvalHover); err != nil || v.Value != "42" {
		t.Errorf("a + b = %+v, %v; want 42", v, err)
	}
	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	end := waitState(t, m, StatusTerminated, 30*time.Second)
	if end.Error != "" {
		t.Errorf("ended with an error: %+v", end)
	}
	// js-debug reports the exit status as the root's stderr, which can land
	// a moment after the state reads terminated.
	for deadline := time.Now().Add(5 * time.Second); !end.HasExitCode && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		end, _ = m.State()
	}
	if !end.HasExitCode || end.ExitCode != 3 {
		t.Errorf("ended as %+v, want exit code 3", end)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		chunks, _, _ := m.Output(0)
		var out strings.Builder
		for _, c := range chunks {
			out.WriteString(c.Text)
		}
		if strings.Contains(out.String(), "total 42") {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("program output not captured: %q", out.String())
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Without a js-debug-adapter launcher on PATH, js-debug's server script is
// looked for where its release is usually unpacked, and run with node.
func TestFindJSDebug(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", t.TempDir())
	if _, _, err := findJSDebug(nil); err == nil || !strings.Contains(err.Error(), "dapDebugServer.js") {
		t.Errorf("nothing installed: err = %v, want where to put it", err)
	}
	script := filepath.Join(data, "indigo", "js-debug", "src", "dapDebugServer.js")
	os.MkdirAll(filepath.Dir(script), 0o755) //nolint:errcheck
	os.WriteFile(script, nil, 0o644)         //nolint:errcheck
	cmd, args, err := findJSDebug([]string{"0", "127.0.0.1"})
	if err != nil || filepath.Base(cmd) != "node" || len(args) != 3 || args[0] != script || args[1] != "0" {
		t.Errorf("got %s %v, %v; want node running the script with the adapter's args", cmd, args, err)
	}
}

// A worker thread is a child of the child: js-debug asks for it with
// startDebugging on the program's own session, and a breakpoint in the
// worker's file stops there, with evaluation in the worker.
func TestWorkerThreadThroughJSDebug(t *testing.T) {
	adapters := jsDebugAdapter(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.js")
	worker := filepath.Join(dir, "worker.js")
	os.WriteFile(main, []byte("const { Worker } = require('node:worker_threads');\nnew Worker(__dirname + '/worker.js');\n"), 0o644) //nolint:errcheck
	os.WriteFile(worker, []byte("const n = 7;\nconsole.log('in worker', n); // line 2\n"), 0o644)                                    //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetAdapters(adapters)
	m.ToggleBreakpoint(worker, 1)
	if err := m.Start(Config{Program: main, Cwd: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Path != worker || st.Line != 1 {
		t.Fatalf("stopped at %s:%d, want %s:1", st.Path, st.Line, worker)
	}
	m.mu.Lock()
	children := len(m.children)
	m.mu.Unlock()
	if children < 2 {
		t.Errorf("%d child sessions, want the program's and the worker's", children)
	}
	if v, err := m.Evaluate("n", 0, dap.EvalHover); err != nil || v.Value != "7" {
		t.Errorf("n = %+v, %v; want 7, evaluated in the worker", v, err)
	}
	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StatusTerminated, 30*time.Second)
}
