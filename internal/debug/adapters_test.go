package debug

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
)

// With no adapter named, a source file goes to the adapter that claims its
// extension and everything else — Go files, package directories — to Delve.
func TestChooseAdapter(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(nil)
	for _, tc := range []struct {
		program, want string
	}{
		{"/w/app.py", "python"},
		{"/w/APP.PY", "python"},
		{"/w/src/server.ts", "node"},
		{"/w/index.mjs", "node"},
		{"/w/main.go", "go"},
		{dir, "go"},
		{"/w/cmd/server", "go"},
	} {
		cfg, err := m.chooseAdapter(Config{Program: tc.program})
		if err != nil || cfg.Adapter != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.program, cfg.Adapter, err, tc.want)
		}
	}
	if _, err := m.chooseAdapter(Config{Program: "/w/x.rb"}); err == nil || !strings.Contains(err.Error(), ".rb") {
		t.Errorf("an unclaimed extension: err = %v, want it named", err)
	}
	// A named adapter is kept, whatever the file.
	if cfg, _ := m.chooseAdapter(Config{Adapter: "lldb", Program: "/w/app.py"}); cfg.Adapter != "lldb" {
		t.Errorf("named adapter replaced: %q", cfg.Adapter)
	}
	// The configured list replaces the defaults.
	m.SetAdapters([]config.DebugAdapter{{Name: "rdbg", Command: "rdbg", Extensions: []string{"rb"}}})
	if cfg, err := m.chooseAdapter(Config{Program: "/w/x.rb"}); err != nil || cfg.Adapter != "rdbg" {
		t.Errorf("configured adapter: %q, %v", cfg.Adapter, err)
	}
}

// For a configured adapter the launch request is its defaults, then what
// indigo knows (program, cwd, args, env), then the configuration's own launch
// table, each winning over the last.
func TestLaunchArgsForAConfiguredAdapter(t *testing.T) {
	a := &config.DebugAdapter{Name: "python", Launch: map[string]any{"console": "internalConsole", "justMyCode": true}}
	got, err := launchArgs(Config{
		Adapter: "python", Program: "/w/app.py", Cwd: "/w", Args: []string{"-v"}, Env: []string{"A=1"},
		Launch: map[string]any{"justMyCode": false, "stopOnEntry": true},
	}, a)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"request": "launch", "program": resolve("/w/app.py"), "cwd": resolve("/w"),
		"args": []string{"-v"}, "env": map[string]string{"A": "1"},
		"console": "internalConsole", "justMyCode": false, "stopOnEntry": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
	if a.Launch["justMyCode"] != true {
		t.Error("building a launch request changed the adapter's defaults")
	}
	// Go takes the configuration's launch table too (Delve's own settings).
	goArgs, _ := launchArgs(Config{Adapter: "go", Program: "/p", Launch: map[string]any{"stopOnEntry": true}}, nil)
	if goArgs["stopOnEntry"] != true || goArgs["outputMode"] != "remote" {
		t.Errorf("go launch = %v", goArgs)
	}
	if _, err := launchArgs(Config{Adapter: "nope"}, nil); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown adapter: err = %v", err)
	}
}

func TestEffectiveDebugAdapters(t *testing.T) {
	c := &config.Config{DebugAdapters: []config.DebugAdapter{{Name: "python", Command: "/venv/bin/python"}}}
	got := c.EffectiveDebugAdapters()
	var names []string
	for _, a := range got {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, []string{"python", "lldb", "node"}) || got[0].Command != "/venv/bin/python" {
		t.Errorf("got %+v, want the user's python replacing the default, then lldb and node", got)
	}
}

// A missing command and an unknown transport are both reported by name.
func TestStartingAConfiguredAdapterFails(t *testing.T) {
	m := NewManager(nil)
	m.launchTimeout = 5 * time.Second
	m.SetAdapters([]config.DebugAdapter{
		{Name: "missing", Command: "indigo-no-such-adapter"},
		{Name: "odd", Command: "sh", Transport: "carrier-pigeon"},
	})
	if err := m.Start(Config{Adapter: "missing", Program: "/p"}); err == nil || !strings.Contains(err.Error(), "indigo-no-such-adapter") {
		t.Errorf("missing command: err = %v", err)
	}
	if err := m.Start(Config{Adapter: "odd", Program: "/p"}); err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("unknown transport: err = %v", err)
	}
}

const cDebuggee = `#include <stdio.h>

int main(void) {
	int x = 42;
	printf("value %d\n", x); /* line 5 (0-based 4) */
	return 3;
}
`

// The generic adapter path end to end, against real lldb-dap over stdio: a
// breakpoint in a C file stops the program there, a local reads back, output
// arrives, and the exit status comes from the "exited" event, which Delve
// never sends.
func TestLLDBDAPDebugsACProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and debugs a program")
	}
	if _, err := findCommand("lldb-dap"); err != nil {
		t.Skip("lldb-dap not installed")
	}
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("no C compiler")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := PermissionHint(); hint != "" {
			t.Skip(hint + " (or set INDIGO_DEBUG_TESTS=1)")
		}
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src, bin := filepath.Join(dir, "prog.c"), filepath.Join(dir, "prog")
	os.WriteFile(src, []byte(cDebuggee), 0o644) //nolint:errcheck
	if out, err := exec.Command(cc, "-g", "-O0", "-o", bin, src).CombinedOutput(); err != nil {
		t.Skipf("cannot compile: %v\n%s", err, out)
	}

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.ToggleBreakpoint(src, 4)
	if err := m.Start(Config{Adapter: "lldb", Program: bin, Cwd: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Path != src || st.Line != 4 {
		t.Errorf("stopped at %s:%d, want %s:4", st.Path, st.Line, src)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	if v, err := m.Evaluate("x", frames[0].ID, dap.EvalHover); err != nil || !strings.Contains(v.Value, "42") {
		t.Errorf("x = %+v, %v; want 42", v, err)
	}
	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	end := waitState(t, m, StatusTerminated, 30*time.Second)
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
		if strings.Contains(out.String(), "value 42") {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("program output not captured: %q", out.String())
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

const pyDebuggee = `import os

def main():
    x = 42
    print("value", x, os.environ.get("INDIGO_PY_ENV"))  # line 5 (0-based 4)

main()
`

// The built-in python adapter against real debugpy: Space d d's shape (no
// adapter named, a .py program) picks it, it stops at a breakpoint, and env
// and output arrive — the output through debugpy's own console, which it uses
// when the client cannot open terminals. Set INDIGO_DEBUGPY_PYTHON to a python with debugpy installed;
// otherwise python3 is tried.
func TestDebugpyDebugsAPythonScript(t *testing.T) {
	if testing.Short() {
		t.Skip("debugs a program")
	}
	python := os.Getenv("INDIGO_DEBUGPY_PYTHON")
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import debugpy").Run(); err != nil {
		t.Skip("debugpy not installed (set INDIGO_DEBUGPY_PYTHON)")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "prog.py")
	os.WriteFile(script, []byte(pyDebuggee), 0o644) //nolint:errcheck

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	adapters := append([]config.DebugAdapter(nil), config.DefaultDebugAdapters...)
	adapters[0].Command = python // the built-in entry, with only the interpreter changed
	m.SetAdapters(adapters)
	m.ToggleBreakpoint(script, 4)
	if err := m.Start(Config{Program: script, Cwd: dir, Env: []string{"INDIGO_PY_ENV=from-config"}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Path != script || st.Line != 4 {
		t.Errorf("stopped at %s:%d, want %s:4", st.Path, st.Line, script)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	if v, err := m.Evaluate("x * 2", frames[0].ID, dap.EvalHover); err != nil || v.Value != "84" {
		t.Errorf("x * 2 = %+v, %v; want 84", v, err)
	}
	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StatusTerminated, 30*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		chunks, _, _ := m.Output(0)
		var out strings.Builder
		for _, c := range chunks {
			out.WriteString(c.Text)
		}
		if strings.Contains(out.String(), "value 42 from-config") {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("program output not captured: %q", out.String())
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}
