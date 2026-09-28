package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/debug"
)

const looperSource = `package main

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

// startLooper builds a Go program that loops and starts it detached from this
// process — through sh, which exits — as a program the user started would be.
// (The server in these tests runs in-process, and the picker leaves out the
// server's own descendants.)
func startLooper(t *testing.T) (src string, pid int) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and debugs a program")
	}
	if _, err := debug.FindDelve(); err != nil {
		t.Skip("dlv not installed")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := debug.PermissionHint(); hint != "" {
			t.Skip(hint + " (or set INDIGO_DEBUG_TESTS=1)")
		}
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	src = filepath.Join(dir, "main.go")
	bin := filepath.Join(dir, "looper")
	os.WriteFile(src, []byte(looperSource), 0o644)                                                      //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/looper\n\ngo 1.21\n"), 0o644) //nolint:errcheck
	build := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command("sh", "-c", bin+" >/dev/null 2>&1 & echo $!").Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) }) //nolint:errcheck
	// sh reports the pid before its child has exec'd the looper: listed in
	// that instant, the process is still sh. Wait until it is the program.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		procs, err := debug.ListProcesses()
		if err != nil {
			t.Fatal(err)
		}
		running := false
		for _, p := range procs {
			if p.PID == pid && p.GoModule == "example.com/looper" {
				running = true
			}
		}
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the looper never showed up as a running Go program")
		}
	}
	return src, pid
}

// attachAndCheck finishes an attach begun in the picker: the program stops at
// the breakpoint, and Shift+F5 detaches, leaving it alive.
func attachAndCheck(t *testing.T, a App, q *pushQueue, pid int) {
	t.Helper()
	// Checked before the stop: stopping opens the program's source in a
	// buffer of its own, and the message stays with the one it arrived in.
	if !strings.HasPrefix(statusOf(a), "Attached to") {
		t.Errorf("status = %q", statusOf(a))
	}
	a = pump(t, a, q, "the attached program to stop", func(a App) bool {
		return a.debug != nil && a.debug.state.Status == client.DebugStopped
	})
	a = pressRun(t, a, "shift+f5")
	pump(t, a, q, "the session to end", func(a App) bool {
		return a.debug.state.Status == client.DebugTerminated
	})
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the program did not survive the detach: %v", err)
	}
}

// Space d a lists the running Go program, labelled with its module; choosing
// it attaches.
func TestAttachThroughThePicker(t *testing.T) {
	src, pid := startLooper(t)
	a, q, rpc, _ := promptApp(t)
	ctx := t.Context()
	if _, err := rpc.ToggleBreakpoint(ctx, src, 9); err != nil {
		t.Fatal(err)
	}
	a = pressRun(t, a, "space", "d", "a")
	a = typeText(t, a, "example.com/looper")
	rows := a.procPicker.rows()
	if len(rows) != 1 || rows[0].proc == nil || rows[0].proc.PID != pid {
		t.Fatalf("picker rows = %+v, want the looper (pid %d)", rows, pid)
	}
	a = pressRun(t, a, "enter")
	attachAndCheck(t, a, q, pid)
}

// A launch.json attach with ${command:pickProcess}: choosing it from Space d l
// opens the picker, titled for the configuration, and attaches to the choice.
func TestPickProcessConfiguration(t *testing.T) {
	src, pid := startLooper(t)
	a, q, rpc, main := promptApp(t)
	ws := filepath.Dir(main)
	os.MkdirAll(filepath.Join(ws, ".vscode"), 0o755) //nolint:errcheck
	err := os.WriteFile(filepath.Join(ws, ".vscode", "launch.json"), []byte(`{"configurations": [
		{"name": "Attach to Go", "type": "go", "request": "attach", "mode": "local", "processId": "${command:pickProcess}"}
	]}`), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.ToggleBreakpoint(t.Context(), src, 9); err != nil {
		t.Fatal(err)
	}
	a = pressRun(t, a, "space", "d", "l")
	a = pressRun(t, a, "1")
	if a.procPicker == nil || a.procPicker.cfg.Name != "Attach to Go" {
		t.Fatalf("picker = %+v, want one for the configuration", a.procPicker)
	}
	a = typeText(t, a, strconv.Itoa(pid))
	a = pressRun(t, a, "enter")
	attachAndCheck(t, a, q, pid)
}
