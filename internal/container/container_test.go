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
	arch            string
	archErr         error
	exists          bool
	existsErr       error
	copyErr         error
	execErr         error
	calls           []string
	copiedFrom      string
	copiedTo        string
	execArgv        []string
	processCounts   map[string]int
	processCountErr error
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

func (f *fakeRuntime) ProcessCount(_ context.Context, _, contains, excluding string) (int, error) {
	f.calls = append(f.calls, "processCount:"+contains+"|"+excluding)
	if f.processCounts != nil {
		return f.processCounts[contains], f.processCountErr
	}
	return 0, f.processCountErr
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

// locateReal writes a small file and points at it, so Attach can hash real
// content — the placement path is derived from it.
func locateReal(t *testing.T, content string) (locate func(string) (string, error), path, hash string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "indigo-server")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return func(string) (string, error) { return path, nil }, path, h
}

func TestAttachCopiesThenStartsTheServer(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: false}
	locate, local, hash := locateReal(t, "a server binary")
	remote := ServerPath("arm64", hash)

	stream, err := Attach(context.Background(), rt, "c1", "/workspaces/proj", AttachOptions{Locate: locate})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	want := []string{"arch", "exists:" + remote, "copy", "exec"}
	if strings.Join(rt.calls, ",") != strings.Join(want, ",") {
		t.Errorf("calls = %v, want %v", rt.calls, want)
	}
	if rt.copiedFrom != local || rt.copiedTo != remote {
		t.Errorf("copied %s -> %s, want %s -> %s", rt.copiedFrom, rt.copiedTo, local, remote)
	}
	// The server is started on the *container's* workspace path, which is the
	// only path it can open anything with.
	wantArgv := []string{remote, "/workspaces/proj"}
	if strings.Join(rt.execArgv, " ") != strings.Join(wantArgv, " ") {
		t.Errorf("exec argv = %v, want %v", rt.execArgv, wantArgv)
	}
}

// TestAttachPlacesDifferentBuildsAtDifferentPaths is the regression for a
// container outliving an indigo upgrade.
//
// The path used to carry only the architecture, so an upgraded client found the
// *previous* server already in place, skipped the copy, and ran it. It surfaced
// as `usage: /tmp/.indigo-server-arm64 <workspace-dir>` — a server from before a
// flag existed, answering a client that passed it.
func TestAttachPlacesDifferentBuildsAtDifferentPaths(t *testing.T) {
	oldLocate, _, oldHash := locateReal(t, "build one")
	newLocate, _, newHash := locateReal(t, "build two")
	if oldHash == newHash {
		t.Fatal("two different binaries hashed the same")
	}

	rt1 := &fakeRuntime{arch: "arm64", exists: false}
	s1, err := Attach(context.Background(), rt1, "c1", "/w", AttachOptions{Locate: oldLocate})
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	s1.Close() //nolint:errcheck

	// The upgraded client asks about a path the old build never occupied, so it
	// copies rather than running what is there.
	rt2 := &fakeRuntime{arch: "arm64", exists: false}
	s2, err := Attach(context.Background(), rt2, "c1", "/w", AttachOptions{Locate: newLocate})
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	s2.Close() //nolint:errcheck

	if rt1.execArgv[0] == rt2.execArgv[0] {
		t.Errorf("both builds ran from %s; an upgrade would keep running the old server", rt1.execArgv[0])
	}
	if !strings.Contains(rt2.execArgv[0], newHash) {
		t.Errorf("started %q, want the new build's path", rt2.execArgv[0])
	}
}

// TestAttachSkipsTheCopyWhenTheServerIsAlreadyThere matters because a second
// window onto the same container would otherwise write 5 MB before it could
// open a file.
func TestAttachSkipsTheCopyWhenTheServerIsAlreadyThere(t *testing.T) {
	rt := &fakeRuntime{arch: "amd64", exists: true}
	locate, _, hash := locateReal(t, "a server binary")

	stream, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locate})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	for _, c := range rt.calls {
		if c == "copy" {
			t.Fatalf("copied although the binary was already present: %v", rt.calls)
		}
	}
	if rt.execArgv[0] != ServerPath("amd64", hash) {
		t.Errorf("started %q, want the amd64 server", rt.execArgv[0])
	}
}

// TestAttachStillNeedsALocalBinaryWhenOneIsAlreadyPlaced records a deliberate
// trade. An earlier version skipped locating when the container already had a
// server, so a host with no built binary could still attach. Content-addressed
// placement ends that: the local binary's hash is what names the path, so it
// has to be read before the question can even be asked.
//
// Worth the loss. The alternative is what it replaced — an upgraded client
// silently running the previous build — and `make install` places the binary
// anyway.
func TestAttachStillNeedsALocalBinaryWhenOneIsAlreadyPlaced(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true}
	locate := func(string) (string, error) { return "", errors.New("nothing built here") }

	_, err := Attach(context.Background(), rt, "c1", "/w", AttachOptions{Locate: locate})
	if err == nil {
		t.Fatal("expected an error with no local binary to hash")
	}
	if !strings.Contains(err.Error(), "nothing built here") {
		t.Errorf("error = %q, want the locate failure surfaced", err)
	}
}

func TestAttachSurfacesEachFailureWithContext(t *testing.T) {
	locate, _, _ := locateReal(t, "a server binary")
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
			_, err := Attach(context.Background(), tc.rt, "c1", "/w", AttachOptions{Locate: locate})
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
	locate := LocateServerBinary
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
	args := execArgs("c1", "", nil, []string{"/tmp/.indigo-server-arm64", "/workspaces/proj"})
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
	// No "--" separator: docker exec treats one as the command to run and
	// fails with `exec: "--": executable file not found`. Verified against a
	// real docker 29.8.0 — an earlier version had one here, and the assertion
	// that it was present encoded the same wrong assumption, so both passed
	// until the feature was run for the first time.
	if contains(args, "--") {
		t.Fatalf("args = %v, want no -- separator; docker exec would try to run it", args)
	}
	idIdx := indexOf(args, "c1")
	if idIdx < 0 {
		t.Fatalf("args = %v, want the container id", args)
	}
	if strings.Join(args[idIdx+1:], " ") != "/tmp/.indigo-server-arm64 /workspaces/proj" {
		t.Errorf("command after the container id = %v, want the server and its workspace", args[idIdx+1:])
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

func TestServerPathDistinguishesArchitectureAndBuild(t *testing.T) {
	if ServerPath("arm64", "abc") == ServerPath("amd64", "abc") {
		t.Error("both architectures share a path; a container reused across hosts would run the wrong binary")
	}
	if ServerPath("arm64", "abc") == ServerPath("arm64", "def") {
		t.Error("two builds share a path; an upgrade would keep running the old server")
	}
	for _, a := range []string{"arm64", "amd64"} {
		if !strings.HasPrefix(ServerPath(a, "abc"), "/tmp/") {
			t.Errorf("ServerPath(%q) = %q, want it under /tmp — a remoteUser may have no home", a, ServerPath(a, "abc"))
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

func TestShouldStopOnExitFollowsTheSpecDefault(t *testing.T) {
	// Absent means stop: that is the specification's default for an image or
	// Dockerfile, and it is what VS Code does. The CLI does not fill it in, so
	// applying it is the tool's job.
	if !(Configuration{}).ShouldStopOnExit() {
		t.Error("an absent shutdownAction should mean stop")
	}
	if !(Configuration{ShutdownAction: "stopContainer"}).ShouldStopOnExit() {
		t.Error("stopContainer should mean stop")
	}
	if !(Configuration{ShutdownAction: "stopCompose"}).ShouldStopOnExit() {
		t.Error("stopCompose should mean stop")
	}
	if (Configuration{ShutdownAction: "none"}).ShouldStopOnExit() {
		t.Error(`only "none" opts out`)
	}
}

// TestServerRunningAsksAboutThisWorkspace: one container can host more than one
// workspace's daemon, so the probe has to name the one being asked about.
func TestServerRunningAsksAboutThisWorkspace(t *testing.T) {
	rt := &fakeRuntime{processCounts: map[string]int{"--daemon /workspaces/proj": 1}}
	running, err := ServerRunning(context.Background(), rt, "c1", "/workspaces/proj")
	if err != nil || !running {
		t.Fatalf("ServerRunning = (%v, %v), want (true, nil)", running, err)
	}
	if got := rt.calls[0]; got != "processCount:--daemon /workspaces/proj|" {
		t.Errorf("probed %q, want it scoped to this workspace", got)
	}
}

// TestOtherWindowsAttachedExcludesThisOne is what makes quitting instant: a
// window has to be able to see past its own bridge, which may still be dying,
// to whether anyone else is there. Without the exclusion the answer would be
// ambiguous and the only way to resolve it would be to wait — which is exactly
// the five-second pause this replaced.
func TestOtherWindowsAttachedExcludesThisOne(t *testing.T) {
	// Two bridges in the container, one of them ours.
	rt := &fakeRuntime{processCounts: map[string]int{
		"/workspaces/proj --client ": 2,
		"--client mytoken":           1,
	}}
	others, err := OtherWindowsAttached(context.Background(), rt, "c1", "/workspaces/proj", "mytoken")
	if err != nil {
		t.Fatalf("OtherWindowsAttached: %v", err)
	}
	if others != 1 {
		t.Errorf("others = %d, want 1", others)
	}

	// Only ours: nobody else, so the container can be stopped.
	rt = &fakeRuntime{processCounts: map[string]int{
		"/workspaces/proj --client ": 1,
		"--client mytoken":           1,
	}}
	others, err = OtherWindowsAttached(context.Background(), rt, "c1", "/workspaces/proj", "mytoken")
	if err != nil || others != 0 {
		t.Errorf("others = (%d, %v), want (0, nil)", others, err)
	}

	// Ours already gone, another still there.
	rt = &fakeRuntime{processCounts: map[string]int{
		"/workspaces/proj --client ": 1,
		"--client mytoken":           0,
	}}
	others, err = OtherWindowsAttached(context.Background(), rt, "c1", "/workspaces/proj", "mytoken")
	if err != nil || others != 1 {
		t.Errorf("others = (%d, %v), want (1, nil)", others, err)
	}
}

// TestOtherWindowsAttachedRefusesWithoutAToken: without one a window cannot
// tell its own bridge from anyone else's, and guessing here means either a
// container left running or one stopped under a live window.
func TestOtherWindowsAttachedRefusesWithoutAToken(t *testing.T) {
	rt := &fakeRuntime{}
	if _, err := OtherWindowsAttached(context.Background(), rt, "c1", "/w", ""); err == nil {
		t.Error("expected an error with no client token")
	}
}

// TestAttachMarksItsBridge pins the other half: the token has to reach the
// process list, or nothing above can see it.
func TestAttachMarksItsBridge(t *testing.T) {
	rt := &fakeRuntime{arch: "arm64", exists: true}
	locate, _, _ := locateReal(t, "a server binary")
	stream, err := Attach(context.Background(), rt, "c1", "/w",
		AttachOptions{ClientToken: "tok123", Locate: locate})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer stream.Close() //nolint:errcheck
	if got := strings.Join(rt.execArgv, " "); !strings.Contains(got, "--client tok123") {
		t.Errorf("exec argv = %q, want it to carry the client token", got)
	}
}
