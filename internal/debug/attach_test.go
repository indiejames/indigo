package debug

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/dap"
)

const loopingProgram = `package main

import (
	"fmt"
	"time"
)

func main() {
	for n := 0; ; n++ {
		fmt.Println("tick", n) // line 10 (0-based 9)
		time.Sleep(50 * time.Millisecond)
	}
}
`

// buildLooper compiles loopingProgram without optimizations (so a breakpoint
// and its locals are where the source says) and returns its directory, the
// source file and the binary.
func buildLooper(t *testing.T) (dir, src, bin string) {
	t.Helper()
	canDebug(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src = filepath.Join(dir, "main.go")
	bin = filepath.Join(dir, "looper")
	os.WriteFile(src, []byte(loopingProgram), 0o644)                                   //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck
	build := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return dir, src, bin
}

// Attaching by process id: the running program stops at a breakpoint, its
// locals read back, and stopping the session detaches — the program goes on
// running, rather than being killed as a launched one would be.
func TestAttachToARunningProcess(t *testing.T) {
	dir, src, bin := buildLooper(t)
	prog := exec.Command(bin)
	stdout, _ := prog.StdoutPipe()
	if err := prog.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { prog.Process.Kill(); prog.Wait() }) //nolint:errcheck
	lines := make(chan string, 1000)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			default:
			}
		}
	}()
	<-lines // running

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.ToggleBreakpoint(src, 9)
	if err := m.Start(Config{Request: "attach", ProcessID: prog.Process.Pid, Cwd: dir}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Path != src || st.Line != 9 {
		t.Errorf("stopped at %s:%d, want %s:9", st.Path, st.Line, src)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 {
		t.Fatalf("stack = %+v, %v", frames, err)
	}
	if _, err := m.Evaluate("n", frames[0].ID, dap.EvalHover); err != nil {
		t.Errorf("evaluate n: %v", err)
	}

	if err := m.Control(ActionStop); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StatusTerminated, 10*time.Second)
	m.Shutdown() // waits for the detach to finish
	// Detached, not killed: still alive, and still printing.
	if err := prog.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the program did not survive the detach: %v", err)
	}
	for len(lines) > 0 {
		<-lines
	}
	for i := 0; i < 5; i++ {
		select {
		case <-lines:
		case <-time.After(5 * time.Second):
			// The process state says which failure this is: T is halted
			// (left stopped by the debugger), R/S is running but silent.
			stat, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(prog.Process.Pid)).Output()
			t.Fatalf("the program is alive but stopped running after the detach (%d lines after it; process state %q)", i, strings.TrimSpace(string(stat)))
		}
	}
}

var apiListening = regexp.MustCompile(`listening at: (\S+:\d+)`)

// Connecting to a debugger someone else started — `dlv debug --headless`,
// as on a remote machine or in a container: no adapter is started, the
// breakpoint stops the program, and stopping the session detaches, leaving
// the headless server and its program running.
func TestConnectToAHeadlessDelve(t *testing.T) {
	dir, src, _ := buildLooper(t)
	dlv, _ := FindDelve()
	server := exec.Command(dlv, "debug", "--headless", "--accept-multiclient", "--api-version=2",
		"--listen=127.0.0.1:0", "--continue")
	server.Dir = dir
	out, _ := server.StdoutPipe()
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Process.Kill(); server.Wait() }) //nolint:errcheck
	addr := make(chan string, 1)
	ticks := make(chan struct{}, 1000) // the program's output comes out through dlv's
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if m := apiListening.FindStringSubmatch(sc.Text()); m != nil {
				addr <- m[1]
			}
			if strings.HasPrefix(sc.Text(), "tick") {
				select {
				case ticks <- struct{}{}:
				default:
				}
			}
		}
	}()
	var a string
	select {
	case a = <-addr:
	case <-time.After(60 * time.Second):
		t.Fatal("dlv --headless never reported its address")
	}
	// Attach to a program that is running, as in real use. Attaching the
	// moment Delve reports its address races Delve's own --continue starting
	// the program, and Delve then fails configurationDone ("debuggee is
	// running") or never answers setBreakpoints — reproduced 1 run in ~10
	// before this wait, not after.
	select {
	case <-ticks:
	case <-time.After(30 * time.Second):
		t.Fatal("the program under dlv --headless never started")
	}

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.ToggleBreakpoint(src, 9)
	if err := m.Start(Config{Request: "attach", Connect: a}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	st := waitState(t, m, StatusStopped, 30*time.Second)
	if st.Path != src || st.Line != 9 {
		t.Errorf("stopped at %s:%d, want %s:9", st.Path, st.Line, src)
	}
	if err := m.Control(ActionStop); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StatusTerminated, 10*time.Second)
	m.Shutdown()
	if err := server.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the headless server did not survive the detach: %v", err)
	}
	// Several lines, not one: a breakpoint left set in the server would halt
	// it again on the next time round the loop.
	for len(ticks) > 0 {
		<-ticks
	}
	for i := 0; i < 5; i++ {
		select {
		case <-ticks:
		case <-time.After(5 * time.Second):
			t.Fatalf("the program stopped running after the detach (%d lines after it)", i)
		}
	}
}

func TestAttachArgs(t *testing.T) {
	for _, tc := range []struct {
		cfg  Config
		want string
	}{
		{Config{Adapter: "go", Request: "attach", ProcessID: 42}, `map[mode:local processId:42 request:attach]`},
		{Config{Adapter: "go", Request: "attach", Connect: "h:2345"}, `map[mode:remote request:attach]`},
	} {
		got, err := launchArgs(tc.cfg, nil)
		if err != nil || strings.TrimSpace(fmtMap(got)) != tc.want {
			t.Errorf("%+v: %v, %v; want %s", tc.cfg, fmtMap(got), err, tc.want)
		}
	}
	if _, err := launchArgs(Config{Adapter: "go", Request: "attach"}, nil); err == nil {
		t.Error("a local attach with no process id was accepted")
	}
}

func fmtMap(m map[string]any) string { return fmt.Sprint(m) }

// An attach that never completes is reported after the attach allowance, not
// a build's, and says it was an attach.
func TestAttachTimesOutQuickly(t *testing.T) {
	m := NewManager(nil)
	m.startAdapter = silentAdapter
	m.attachTimeout = 200 * time.Millisecond // the launch allowance stays at minutes
	err := m.Start(Config{Adapter: "go", Request: "attach", ProcessID: 1})
	if err == nil || !strings.Contains(err.Error(), "did not attach within 200ms") {
		t.Fatalf("err = %v, want an attach timeout", err)
	}
}
