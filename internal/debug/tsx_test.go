package debug

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
)

// esmProgram is shaped like a real user's: an ES module ("type": "module")
// with a top-level await loop. For an ES module tsx compiles in Node's loader
// thread, which is what defeated every fix that reached only the main thread.
const esmProgram = "const sleep = (ms: number): Promise<void> => {\n  return new Promise((resolve) => setTimeout(resolve, ms));\n};\n\nconst x = 4;\nlet i = 0;\nwhile (true) {\n  const z = x + i;\n  i++;\n  console.log(`z = ${z}`);\n  await sleep(100);\n}\n"

func tsxProject(t *testing.T) (dir, file string) {
	t.Helper()
	if _, err := exec.LookPath("tsx"); err != nil {
		t.Skip("tsx not installed")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir()) // the preload goes here, not in the real home
	dir, _ = filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(dir, "src"), 0o755) //nolint:errcheck
	file = filepath.Join(dir, "src", "index.ts")
	os.WriteFile(file, []byte(esmProgram), 0o644)                                        //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"type":"module"}`), 0o644) //nolint:errcheck
	return dir, file
}

// Launching through tsx binds breakpoints in the .ts file with no extra
// setting: indigo loads its preload through NODE_OPTIONS. Without it this
// program runs straight past the breakpoint (checked, before the preload).
func TestTSXLaunchBindsBreakpoints(t *testing.T) {
	adapters := jsDebugAdapter(t)
	dir, file := tsxProject(t)
	writeProjectLaunches(t, dir, "[[debug]]\nname = \"index\"\nprogram = \"src/index.ts\"\nlaunch = { runtimeExecutable = \"tsx\" }\n")
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetAdapters(adapters)
	cfgs, err := m.Launches(dir, nil, "")
	if err != nil || len(cfgs) != 1 {
		t.Fatalf("configs = %+v, %v", cfgs, err)
	}
	m.ToggleBreakpoint(file, 8) // i++
	if err := m.Start(cfgs[0]); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, m, StatusStopped, 30*time.Second)
	if st.Path != file || st.Line != 8 {
		t.Errorf("stopped at %s:%d, want %s:8", st.Path, st.Line, file)
	}
	if bps, _ := m.Breakpoints.List(file); !bps[0].Verified {
		t.Errorf("breakpoint %+v not shown as set after stopping on it", bps[0])
	}
}

// Attaching to a tsx program started with the preload binds its breakpoints,
// and — being fixed — draws no warning; one started without it is warned
// about, with the fix spelled out.
func TestAttachToTSXWithPreload(t *testing.T) {
	adapters := jsDebugAdapter(t)
	dir, file := tsxProject(t)
	preload, err := TSXPreloadPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	saved := nodeInspectorAddr
	nodeInspectorAddr = ln.Addr().String()
	ln.Close() //nolint:errcheck
	t.Cleanup(func() { nodeInspectorAddr = saved })
	_, port, _ := net.SplitHostPort(nodeInspectorAddr)

	start := func(env ...string) int {
		cmd := exec.Command("tsx", "--inspect-port="+port, "src/index.ts")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() }) //nolint:errcheck
		// tsx runs the program in a child node process; that is the one to
		// attach to. Killing the tsx CLI alone would orphan it.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			raw, _ := listRawProcesses()
			for _, p := range raw {
				if p.ppid == cmd.Process.Pid && p.name == "node" {
					child := p.pid
					t.Cleanup(func() { exec.Command("kill", "-9", strconv.Itoa(child)).Run() }) //nolint:errcheck
					return child
				}
			}
		}
		t.Fatal("tsx never started its program")
		return 0
	}

	bare := start()
	if w := tsxWarning(bare); !strings.Contains(w, `NODE_OPTIONS="--require `+preload+`"`) {
		t.Errorf("warning for tsx without the preload = %q, want the fix with the preload's path", w)
	}
	exec.Command("kill", "-9", strconv.Itoa(bare)).Run() //nolint:errcheck

	fixed := start("NODE_OPTIONS=--require " + preload)
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetAdapters(adapters)
	m.ToggleBreakpoint(file, 8)
	warning, err := m.StartWithWarning(Config{Adapter: "node", Request: "attach", ProcessID: fixed, Cwd: dir})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if warning != "" {
		t.Errorf("warned about a tsx program started with the preload: %q", warning)
	}
	st := waitState(t, m, StatusStopped, 30*time.Second)
	if st.Path != file || st.Line != 8 {
		t.Errorf("stopped at %s:%d, want %s:8", st.Path, st.Line, file)
	}
}

func TestWithTSXPreload(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	preload, _ := TSXPreloadPath()
	tsx := map[string]any{"runtimeExecutable": "tsx"}
	got := withTSXPreload(Config{Launch: tsx, Env: []string{"A=1", "NODE_OPTIONS=--max-old-space-size=4096"}})
	want := []string{"A=1", "NODE_OPTIONS=--max-old-space-size=4096 --require " + preload}
	if !reflect.DeepEqual(got.Env, want) {
		t.Errorf("kept NODE_OPTIONS: %q, want %q", got.Env, want)
	}
	if got := withTSXPreload(Config{Launch: map[string]any{"runtimeExecutable": "/usr/local/bin/tsx"}}); !reflect.DeepEqual(got.Env, []string{"NODE_OPTIONS=--require " + preload}) {
		t.Errorf("tsx by path: %q", got.Env)
	}
	if again := withTSXPreload(Config{Launch: tsx, Env: want}); !reflect.DeepEqual(again.Env, want) {
		t.Errorf("added twice: %q", again.Env)
	}
	for _, cfg := range []Config{
		{Launch: map[string]any{"runtimeExecutable": "node"}},
		{Request: "attach", Launch: tsx},
		{},
	} {
		if got := withTSXPreload(cfg); got.Env != nil {
			t.Errorf("%+v got %q, want nothing added", cfg, got.Env)
		}
	}
	if data, _ := os.ReadFile(preload); !strings.Contains(string(data), "delete Error.prepareStackTrace;") {
		t.Errorf("preload file = %q", data)
	}
}

// ${workspaceFolder} is replaced in env values and anywhere in launch, not
// just program/cwd/args: js-debug handed one it cannot resolve drops the
// connection.
func TestWorkspaceFolderInEnvAndLaunch(t *testing.T) {
	cfg := resolveLaunch("/w", config.DebugLaunch{
		Name:   "x",
		Env:    map[string]string{"NODE_OPTIONS": "--require ${workspaceFolder}/p.cjs"},
		Launch: map[string]any{"outFiles": []any{"${workspaceFolder}/dist/**/*.js"}, "nested": map[string]any{"root": "${workspaceFolder}"}},
	})
	if !reflect.DeepEqual(cfg.Env, []string{"NODE_OPTIONS=--require /w/p.cjs"}) {
		t.Errorf("env = %q", cfg.Env)
	}
	if !reflect.DeepEqual(cfg.Launch, map[string]any{"outFiles": []any{"/w/dist/**/*.js"}, "nested": map[string]any{"root": "/w"}}) {
		t.Errorf("launch = %v", cfg.Launch)
	}
}

// A process's environment is read back. With node, not /bin/sleep: macOS
// withholds the environment of its own protected system binaries, which is
// fine here — the processes this is for are the user's Node programs.
func TestProcessEnv(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	cmd := exec.Command("node", "-e", "setTimeout(() => {}, 30000)")
	cmd.Env = append(os.Environ(), "INDIGO_ENV_PROBE=found")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() }) //nolint:errcheck
	time.Sleep(100 * time.Millisecond)
	for _, kv := range processEnv(cmd.Process.Pid) {
		if kv == "INDIGO_ENV_PROBE=found" {
			return
		}
	}
	t.Error("the variable was not read back")
}
