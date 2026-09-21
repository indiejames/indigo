package container

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/procutil"
)

// fakeRuntime records what Attach asked it to do, in order. The whole point of
// the Runtime interface is that this sequence is assertable on a machine with
// no container engine, which is where this was written.
type fakeRuntime struct {
	arch       string
	archErr    error
	exists     bool
	existsErr  error
	copyErr    error
	execErr    error
	calls      []string
	copiedFrom string
	copiedTo   string
	execArgv   []string
}

func (f *fakeRuntime) Arch(context.Context, string) (string, error) {
	f.calls = append(f.calls, "arch")
	return f.arch, f.archErr
}

func (f *fakeRuntime) FileExists(_ context.Context, _, path string) (bool, error) {
	f.calls = append(f.calls, "exists:"+path)
	return f.exists, f.existsErr
}

func (f *fakeRuntime) CopyIn(_ context.Context, _, localPath, remotePath string) error {
	f.calls = append(f.calls, "copy")
	f.copiedFrom, f.copiedTo = localPath, remotePath
	return f.copyErr
}

func (f *fakeRuntime) Exec(_ context.Context, _ string, argv []string) (io.ReadWriteCloser, error) {
	f.calls = append(f.calls, "exec")
	f.execArgv = argv
	if f.execErr != nil {
		return nil, f.execErr
	}
	return nopStream{}, nil
}

type nopStream struct{}

func (nopStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (nopStream) Write(b []byte) (int, error) { return len(b), nil }
func (nopStream) Close() error                { return nil }

func locateFixed(path string) func(string) (string, error) {
	return func(string) (string, error) { return path, nil }
}

func TestAttachCopiesThenStartsTheServer(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: false}

	stream, err := Attach(context.Background(), rt, "c1", "/workspaces/proj", AttachOptions{Locate: locateFixed("/local/indigo-server-linux-arm64")})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	want := []string{"arch", "exists:" + ServerPath("arm64"), "copy", "exec"}
	if strings.Join(rt.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", rt.calls, want)
	}
	if rt.copiedFrom != "/local/indigo-server-linux-arm64" || rt.copiedTo != ServerPath("arm64") {
		t.Errorf("copied %s -> %s, want the arm64 binary to %s", rt.copiedFrom, rt.copiedTo, ServerPath("arm64"))
	}
	// The server is started on the *container's* workspace path, which is the
	// only path it can open anything with.
	wantArgv := []string{ServerPath("arm64"), "/workspaces/proj"}
	if strings.Join(rt.execArgv, " ") != strings.Join(wantArgv, " ") {
		t.Errorf("exec argv = %v, want %v", rt.execArgv, wantArgv)
	}
}

// TestAttachSkipsTheCopyWhenTheServerIsAlreadyThere matters because a second
// window onto the same container would otherwise write 5 MB before it could
// open a file.
func TestAttachSkipsTheCopyWhenTheServerIsAlreadyThere(t *testing.T) {
	rt := &fakeRuntime{arch: "amd64", exists: true}

	stream, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locateFixed("/local/x")})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	for _, c := range rt.calls {
		if c == "copy" {
			t.Fatalf("copied although the binary was already present: %v", rt.calls)
		}
	}
	if rt.execArgv[0] != ServerPath("amd64") {
		t.Errorf("started %q, want the amd64 server", rt.execArgv[0])
	}
}

// TestAttachDoesNotLocateABinaryItWillNotUse pins the ordering: asking where a
// binary is has to come after learning it is needed, or a host with no built
// server could not attach to a container that already has one.
func TestAttachDoesNotLocateABinaryItWillNotUse(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true}
	called := false
	locate := func(string) (string, error) {
		called = true
		return "", errors.New("should not be consulted")
	}
	if _, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locate}); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if called {
		t.Error("looked for a local binary although the container already had one")
	}
}

func TestAttachSurfacesEachFailureWithContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		rt   *fakeRuntime
		want string
	}{
		{"arch", &fakeRuntime{archErr: errors.New("boom")}, "architecture"},
		{"exists", &fakeRuntime{arch: "arm64", existsErr: errors.New("boom")}, "check for"},
		{"copy", &fakeRuntime{arch: "arm64", copyErr: errors.New("boom")}, "copy server"},
		{"exec", &fakeRuntime{arch: "arm64", exists: true, execErr: errors.New("boom")}, "start server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Attach(context.Background(), tc.rt, "c1", "/w", AttachOptions{Locate: locateFixed("/local/x")})
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q — a bare %q says nothing about which step failed",
					err, tc.want, "boom")
			}
		})
	}
}

func TestAttachReportsAMissingLocalBinary(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: false}
	locate := func(goarch string) (string, error) {
		return LocateServerBinary(goarch)
	}
	t.Setenv("INDIGO_CONTAINER_SERVER", "")
	t.Setenv("HOME", t.TempDir())

	_, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locate})
	if err == nil {
		t.Skip("a server binary happens to be installed beside the test binary")
	}
	if !strings.Contains(err.Error(), "make build-container-server") {
		t.Errorf("error = %q, want it to name the make target; %q on its own is a dead end",
			err, "not found")
	}
}

// TestExecArgsNeverAllocateATTY is the one assertion in this package that
// guards a silent corruption rather than a visible failure. A pty rewrites \n
// as \r\n, and capnp is binary framing — so `docker exec -t` does not break the
// connection, it quietly mangles any message containing that byte. Adding -t is
// a one-character change with a symptom nobody would trace back here.
func TestExecArgsNeverAllocateATTY(t *testing.T) {
	args := execArgs("c1", "", []string{"/tmp/.indigo-server-arm64", "/workspaces/proj"})
	for _, a := range args {
		if a == "-t" || a == "--tty" || a == "-it" || a == "-ti" {
			t.Fatalf("execArgs allocated a TTY: %v", args)
		}
	}
	if args[0] != "exec" {
		t.Errorf("args = %v, want an exec", args)
	}
	// stdin must be attached: it is half the wire.
	if !contains(args, "-i") {
		t.Errorf("args = %v, want -i", args)
	}
	// "--" keeps a command starting with a dash from being read as a flag.
	sep := indexOf(args, "--")
	if sep < 0 {
		t.Fatalf("args = %v, want a -- separator", args)
	}
	if indexOf(args, "c1") > sep {
		t.Errorf("args = %v, want the container id before --", args)
	}
	if strings.Join(args[sep+1:], " ") != "/tmp/.indigo-server-arm64 /workspaces/proj" {
		t.Errorf("command after -- = %v, want the server and its workspace", args[sep+1:])
	}
}

func TestCopyArgsAddressTheContainer(t *testing.T) {
	args := copyArgs("c1", "/local/bin", "/tmp/.indigo-server-arm64")
	want := "cp /local/bin c1:/tmp/.indigo-server-arm64"
	if strings.Join(args, " ") != want {
		t.Errorf("copyArgs = %q, want %q", strings.Join(args, " "), want)
	}
}

func TestLocateServerBinaryPrefersAnExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "my-server")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INDIGO_CONTAINER_SERVER", p)

	got, err := LocateServerBinary("arm64")
	if err != nil {
		t.Fatalf("LocateServerBinary: %v", err)
	}
	if got != p {
		t.Errorf("located %q, want the override %q", got, p)
	}
}

func TestServerPathIsArchitectureStamped(t *testing.T) {
	if ServerPath("arm64") == ServerPath("amd64") {
		t.Fatal("both architectures share a path; a container reused across hosts would run the wrong binary")
	}
	for _, a := range []string{"arm64", "amd64"} {
		if !strings.HasPrefix(ServerPath(a), "/tmp/") {
			t.Errorf("ServerPath(%q) = %q, want it under /tmp — a remoteUser may have no home", a, ServerPath(a))
		}
	}
}

func contains(ss []string, s string) bool { return indexOf(ss, s) >= 0 }

func indexOf(ss []string, s string) int {
	for i, v := range ss {
		if v == s {
			return i
		}
	}
	return -1
}

// TestProcStreamCloseIsIdempotentAndConcurrencySafe is a regression test for a
// deadlock, not a tidiness point.
//
// Close has two callers that know nothing about each other: whoever opened the
// stream, and capnp, which owns it from client.DialStream onwards and closes it
// when the connection tears down. os/exec's Cmd.Wait is not safe to call twice
// — a second concurrent call parks forever waiting on the first — so an
// ordinary shutdown deadlocked one of the two. It showed up as a test that
// hung in Close with no output at all.
func TestProcStreamCloseIsIdempotentAndConcurrencySafe(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 60")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a process here: %v", err)
	}
	p := &procStream{cmd: cmd, in: in, out: out}

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			p.Close() //nolint:errcheck
			done <- struct{}{}
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent Close deadlocked")
		}
	}
	if cmd.ProcessState == nil {
		t.Error("the process was not reaped")
	}
}

// TestProcStreamCloseKillsTheWholeProcessTree is the lesson internal/procutil
// exists for, applied here: the server spawns plugin processes of its own, and
// killing only the direct child orphans them. Inside a container they would
// linger holding the exec's stdio; in the test suite they held the test
// binary's, which surfaced as "Test I/O incomplete" on a loaded machine and
// nothing at all otherwise.
//
// The assertion is on the grandchild's pid, not on the pipe reaching EOF. An
// earlier version watched the pipe and passed with the fix reverted, because
// Close closes our own read end either way — it was detecting itself.
func TestProcStreamCloseKillsTheWholeProcessTree(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $! > "+pidFile+"; sleep 60")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	procutil.SetPgid(cmd)
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a process here: %v", err)
	}
	p := &procStream{cmd: cmd, in: in, out: out}
	t.Cleanup(func() { p.Close() }) //nolint:errcheck

	var pid int
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Skip("the grandchild never reported its pid")
	}
	// Signal 0 tests for existence without delivering anything.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Skipf("the grandchild was not running to begin with: %v", err)
	}

	p.Close() //nolint:errcheck

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone, as it must be
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck
	t.Fatalf("grandchild %d outlived the kill; only the direct child was signalled", pid)
}
