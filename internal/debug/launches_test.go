package debug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
)

func writeProjectLaunches(t *testing.T, root, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".indigo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ProjectLaunchFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A configuration as written becomes one ready to start: paths against the
// workspace, ${workspaceFolder} expanded, defaults filled, env in a stable
// order.
func TestLaunchesResolvesAgainstTheWorkspace(t *testing.T) {
	root := t.TempDir()
	writeProjectLaunches(t, root, `
[[debug]]
name = "server"
program = "./cmd/server"
args = ["--config", "${workspaceFolder}/dev.toml"]
cwd = "."
build_flags = "-tags dev"
env = { ZED = "1", ALPHA = "a=b" }

[[debug]]
name = "abs"
program = "/elsewhere/tool"
mode = "test"
`)
	got, err := NewManager(nil).Launches(root, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Config{
		{
			Name: "server", Mode: "debug",
			Program:    filepath.Join(root, "cmd/server"),
			Args:       []string{"--config", root + "/dev.toml"},
			Cwd:        root,
			BuildFlags: "-tags dev",
			Env:        []string{"ALPHA=a=b", "ZED=1"},
		},
		{Name: "abs", Mode: "test", Program: "/elsewhere/tool"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

// The workspace's file lists first and wins a name clash with config.toml;
// a program left out means the workspace itself.
func TestLaunchesWorkspaceShadowsGlobal(t *testing.T) {
	root := t.TempDir()
	writeProjectLaunches(t, root, "[[debug]]\nname = \"run\"\nprogram = \"./a\"\n")
	global := []config.DebugLaunch{{Name: "run", Program: "./b"}, {Name: "all"}}
	got, err := NewManager(nil).Launches(root, global, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "run" || got[0].Program != filepath.Join(root, "a") {
		t.Fatalf("got %+v, want the workspace's run first", got)
	}
	if got[1].Name != "all" || got[1].Program != root {
		t.Errorf("got %+v, want all debugging the workspace root", got[1])
	}
}

// A broken file is reported, and does not hide the configurations that did
// load. Unnamed and duplicate entries are reported too, not silently dropped.
func TestLaunchesReportsProblemsWithoutHidingTheRest(t *testing.T) {
	root := t.TempDir()
	writeProjectLaunches(t, root, "[[debug]\nname = ")
	got, err := NewManager(nil).Launches(root, []config.DebugLaunch{{Name: "g"}}, "")
	if err == nil || !strings.Contains(err.Error(), ProjectLaunchFile) {
		t.Errorf("err = %v, want the broken file named", err)
	}
	if len(got) != 1 || got[0].Name != "g" {
		t.Errorf("got %+v, want the global config still offered", got)
	}

	writeProjectLaunches(t, root, "[[debug]]\nprogram = \".\"\n[[debug]]\nname = \"x\"\n[[debug]]\nname = \"x\"\n")
	got, err = NewManager(nil).Launches(root, []config.DebugLaunch{{Name: "y"}, {Name: "y"}}, "")
	for _, want := range []string{"has no name", `two [[debug]] entries are named "x"`, `config.toml: two [[debug]] entries are named "y"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %+v, want x and y once each", got)
	}
}

func TestLaunchesWithNoFiles(t *testing.T) {
	got, err := NewManager(nil).Launches(t.TempDir(), nil, "")
	if err != nil || len(got) != 0 {
		t.Errorf("got %+v, %v; want nothing and no error", got, err)
	}
}

func TestLaunchArgsCarriesEnv(t *testing.T) {
	args, err := launchArgs(Config{Adapter: "go", Program: "/p", Env: []string{"A=1", "B=x=y"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"A": "1", "B": "x=y"}; !reflect.DeepEqual(args["env"], want) {
		t.Errorf("env = %v, want %v", args["env"], want)
	}
	if _, err := launchArgs(Config{Adapter: "go", Program: "/p", Env: []string{"NOEQUALS"}}, nil); err == nil {
		t.Error("an env entry without = was accepted")
	}
}

// recordingAdapter fails every launch at once, recording what it was asked to
// start.
type recordingAdapter struct {
	mu      sync.Mutex
	started []Config
}

func (r *recordingAdapter) start(_ context.Context, cfg Config, _ func(dap.Event)) (*dap.Client, error) {
	r.mu.Lock()
	r.started = append(r.started, cfg)
	r.mu.Unlock()
	return nil, errors.New("recorded")
}

func (r *recordingAdapter) programs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.started {
		out = append(out, c.Program)
	}
	return out
}

// Restart starts the fallback when nothing has run, and afterwards the most
// recent configuration — even one whose launch failed, since trying again
// after fixing the build is the usual reason to restart.
func TestRestartRerunsTheLastConfiguration(t *testing.T) {
	m := NewManager(nil)
	rec := &recordingAdapter{}
	m.startAdapter = rec.start

	if _, err := m.Restart(Config{}); err == nil || !strings.Contains(err.Error(), "nothing to restart") {
		t.Errorf("err = %v, want nothing to restart", err)
	}
	started, _ := m.Restart(Config{Adapter: "go", Program: "/fallback"})
	if started.Program != "/fallback" {
		t.Errorf("started %+v, want the fallback", started)
	}
	m.Start(Config{Adapter: "go", Program: "/chosen", Name: "chosen"}) //nolint:errcheck // fails by design
	started, _ = m.Restart(Config{Adapter: "go", Program: "/fallback"})
	if started.Name != "chosen" {
		t.Errorf("started %+v, want the last configuration", started)
	}
	if got, want := rec.programs(), []string{"/fallback", "/chosen", "/chosen"}; !reflect.DeepEqual(got, want) {
		t.Errorf("launched %v, want %v", got, want)
	}
}

// Restart during a session stops it first; the new launch is not refused as
// "already running", and the old Start returns rather than hanging.
func TestRestartStopsTheRunningSession(t *testing.T) {
	m := NewManager(nil)
	m.startAdapter = silentAdapter
	m.launchTimeout = 30 * time.Second
	firstDone := make(chan error, 1)
	go func() { firstDone <- m.Start(Config{Adapter: "go", Program: "/first"}) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		up := m.client != nil
		m.mu.Unlock()
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first session never came up")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// A second Start while the first is still launching is refused.
	if err := m.Start(Config{Adapter: "go", Program: "/second"}); !errors.Is(err, ErrSessionActive) {
		t.Errorf("Start during a launch: err = %v, want ErrSessionActive", err)
	}

	rec := &recordingAdapter{}
	m.mu.Lock()
	m.startAdapter = rec.start
	m.mu.Unlock()
	started, err := m.Restart(Config{})
	if errors.Is(err, ErrSessionActive) {
		t.Fatal("restart was refused as already running")
	}
	if started.Program != "/first" {
		t.Errorf("restarted %+v, want /first", started)
	}
	if got := rec.programs(); !reflect.DeepEqual(got, []string{"/first"}) {
		t.Errorf("launched %v, want /first again", got)
	}
	select {
	case <-firstDone:
	case <-time.After(5 * time.Second):
		t.Error("the stopped session's Start never returned")
	}
}

const twoTests = `package d

import (
	"os"
	"testing"
)

func TestA(t *testing.T) {
	a := 1
	t.Log(a) // line 10 (0-based 9)
}

func TestB(t *testing.T) {
	got := os.Getenv("INDIGO_E2E_ENV")
	t.Log(got) // line 15 (0-based 14)
}
`

// Against real Delve: selecting one test with -test.run stops in that test and
// not in the one before it, and a configuration's env reaches the program.
func TestSingleTestWithEnvAgainstDelve(t *testing.T) {
	canDebug(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "d_test.go")
	os.WriteFile(file, []byte(twoTests), 0o644)                                        //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.ToggleBreakpoint(file, 9)
	m.ToggleBreakpoint(file, 14)
	err = m.Start(Config{
		Adapter: "go", Mode: "test", Program: dir, Cwd: dir,
		Args: []string{"-test.run", "^TestB$"},
		Env:  []string{"INDIGO_E2E_ENV=from-config"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var st State
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		st, _ = m.State()
		if st.Status == StatusStopped {
			break
		}
		if st.Status == StatusTerminated || time.Now().After(deadline) {
			bps, _ := m.Breakpoints.List(file)
			chunks, _, _ := m.Output(0)
			t.Fatalf("never stopped: %+v\nbreakpoints %+v\noutput %+v", st, bps, chunks)
		}
	}
	if st.Line != 14 {
		t.Fatalf("stopped at line %d, want 14 (TestB only)", st.Line+1)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 {
		t.Fatal(err)
	}
	if v, err := m.Evaluate("got", frames[0].ID, dap.EvalHover); err != nil || v.Value != `"from-config"` {
		t.Errorf("got = %+v, %v; want the configured env", v, err)
	}
}

// A Start while another is still bringing its adapter up — before the manager
// has a client to show for it — is refused, not run as a second debugger.
func TestStartRefusedWhileAdapterIsStarting(t *testing.T) {
	m := NewManager(nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls int
	var mu sync.Mutex
	var once sync.Once
	m.startAdapter = func(context.Context, Config, func(dap.Event)) (*dap.Client, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil, errors.New("released")
	}
	done := make(chan struct{})
	go func() { m.Start(Config{Adapter: "go", Program: "/a"}); close(done) }() //nolint:errcheck
	<-entered
	err := m.Start(Config{Adapter: "go", Program: "/b"})
	close(release)
	<-done
	if !errors.Is(err, ErrSessionActive) {
		t.Errorf("second Start: err = %v, want ErrSessionActive", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("adapter started %d times, want 1", calls)
	}
}
