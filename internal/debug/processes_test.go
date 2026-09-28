package debug

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// buildGoBinary compiles a trivial Go program that sleeps, returning its path.
func buildGoBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a program")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Minute) }\n"), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/sleeper\n\ngo 1.21\n"), 0o644)                                       //nolint:errcheck
	bin := filepath.Join(dir, "sleeper")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func startProc(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() }) //nolint:errcheck
	return cmd
}

// The real process table: a running Go program is listed with its module and
// toolchain, a non-Go program is listed as such, and arguments come through
// whole — "two words" is one argument, not two.
func TestListProcessesFindsAGoProgram(t *testing.T) {
	bin := buildGoBinary(t)
	goProc := startProc(t, bin, "--flag", "two words")
	sleep := startProc(t, "sleep", "60")
	var got []Process
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := listRawProcesses()
		if err != nil {
			t.Fatal(err)
		}
		// Filtered as if from outside: both are this test's children, which
		// ListProcesses leaves out as the server's own.
		got = filterProcesses(raw, os.Getuid(), -1)
		if find(got, goProc.Process.Pid) != nil && find(got, sleep.Process.Pid) != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("started processes not listed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	g := find(got, goProc.Process.Pid)
	if !g.IsGo() || g.GoModule != "example.com/sleeper" || !strings.HasPrefix(g.GoVersion, "go") {
		t.Errorf("Go program = %+v, want its module and version", g)
	}
	if g.Name != "sleeper" || !reflect.DeepEqual(g.Args, []string{"--flag", "two words"}) {
		t.Errorf("name/args = %q %q", g.Name, g.Args)
	}
	if s := find(got, sleep.Process.Pid); s.IsGo() || s.Name != "sleep" {
		t.Errorf("sleep = %+v, want a non-Go process named sleep", s)
	}
	// Go programs sort first.
	sawOther := false
	for _, p := range got {
		if !p.IsGo() {
			sawOther = true
		} else if sawOther {
			t.Fatalf("a Go program (%s) sorts after a non-Go one", p.Name)
		}
	}
	// And from ListProcesses itself, its own children are left out.
	listed, err := ListProcesses()
	if err != nil {
		t.Fatal(err)
	}
	if find(listed, goProc.Process.Pid) != nil || find(listed, os.Getpid()) != nil {
		t.Error("ListProcesses listed itself or a process it started")
	}
}

func find(ps []Process, pid int) *Process {
	for i := range ps {
		if ps[i].PID == pid {
			return &ps[i]
		}
	}
	return nil
}

// Other users' processes, the process itself and everything it started
// (however deep) are left out.
func TestFilterProcesses(t *testing.T) {
	raw := []rawProcess{
		{pid: 1, ppid: 0, uid: 0, name: "init"},
		{pid: 10, ppid: 1, uid: 501, name: "shell"},
		{pid: 20, ppid: 10, uid: 501, name: "indigo-server"}, // self
		{pid: 21, ppid: 20, uid: 501, name: "gopls"},
		{pid: 22, ppid: 21, uid: 501, name: "gopls-child"},
		{pid: 30, ppid: 10, uid: 501, name: "app"},
		{pid: 40, ppid: 1, uid: 0, name: "sshd"},
	}
	var names []string
	for _, p := range filterProcesses(raw, 501, 20) {
		names = append(names, p.Name)
	}
	if !reflect.DeepEqual(names, []string{"app", "shell"}) {
		t.Errorf("got %v, want app and shell", names)
	}
}

// The Linux reader, against a /proc laid out the way the kernel lays it out:
// the executable through the exe link (which is how the Go check finds the
// binary), the full name from it rather than status's 15-character one, and
// arguments split on NULs.
func TestListProcFS(t *testing.T) {
	bin := buildGoBinary(t)
	root := t.TempDir()
	proc := filepath.Join(root, "4242")
	os.MkdirAll(proc, 0o755)                                                                                                //nolint:errcheck
	os.WriteFile(filepath.Join(proc, "status"), []byte("Name:\tsleeper-with-a-l\nState:\tS\nPPid:\t7\nUid:\t501\n"), 0o644) //nolint:errcheck
	os.WriteFile(filepath.Join(proc, "cmdline"), []byte(bin+"\x00-v\x00two words\x00"), 0o644)                              //nolint:errcheck
	os.Symlink(bin, filepath.Join(proc, "exe"))                                                                             //nolint:errcheck
	os.MkdirAll(filepath.Join(root, "self"), 0o755)                                                                         //nolint:errcheck
	os.MkdirAll(filepath.Join(root, "999"), 0o755)                                                                          //nolint:errcheck // exited: nothing readable
	raw, err := listProcFS(root)
	if err != nil {
		t.Fatal(err)
	}
	var r *rawProcess
	for i := range raw {
		if raw[i].pid == 4242 {
			r = &raw[i]
		}
	}
	if r == nil {
		t.Fatalf("pid 4242 not read: %+v", raw)
	}
	if r.ppid != 7 || r.exe != bin || r.name != "sleeper" || r.uid != os.Getuid() ||
		!reflect.DeepEqual(r.args, []string{"-v", "two words"}) {
		t.Errorf("got %+v", *r)
	}
	ps := filterProcesses(raw, os.Getuid(), -1)
	if p := find(ps, 4242); p == nil || p.GoModule != "example.com/sleeper" {
		t.Errorf("not recognised as a Go program: %+v", p)
	}
}

// A stopped process is recognised as stopped, and unstickAfterDetach resumes
// it — what a Delve detach that leaves its process stopped needs — while a
// running one is not signalled at all.
func TestUnstickAfterDetach(t *testing.T) {
	bin := buildGoBinary(t) // a program of ours: sleep(1) is an Apple binary on macOS
	p := startProc(t, bin)
	time.Sleep(200 * time.Millisecond)
	if processStopped(p.Process.Pid) {
		t.Fatal("a running process reads as stopped")
	}
	if err := p.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !processStopped(p.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatal("a stopped process does not read as stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	unstickAfterDetach(p.Process.Pid)
	if processStopped(p.Process.Pid) {
		t.Error("still stopped after unstickAfterDetach")
	}
}
