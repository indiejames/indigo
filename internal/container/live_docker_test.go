package container

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
)

// These exercise the one part of this package that a fake cannot: the real
// docker CLI. They skip unless a runtime is actually reachable, so an ordinary
// `go test ./...` on a machine without one is unaffected.
//
// They earn their keep because everything they cover was, until it was run,
// written from documentation: whether `docker exec` without -t really leaves the
// stream untouched, whether a CGO_ENABLED=0 binary really runs on a musl image,
// and whether a server started this way can serve a full editing session.

// liveRuntime skips unless docker answers, and returns it.
func liveRuntime(t *testing.T) Docker {
	t.Helper()
	path, _, err := RuntimePath()
	if err != nil {
		t.Skipf("no container runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, path, "ps").Run(); err != nil {
		t.Skipf("container runtime present but not answering: %v", err)
	}
	return Docker{Command: path}
}

// startLiveContainer runs a throwaway container with hostDir bind-mounted at
// /workspace, and removes it afterwards.
func startLiveContainer(t *testing.T, d Docker, image, hostDir string, extraArgs ...string) string {
	t.Helper()
	name := "indigo-livetest-" + strings.ReplaceAll(t.Name(), "/", "-")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	exec.CommandContext(ctx, d.bin(), "rm", "-f", name).Run() //nolint:errcheck

	args := append([]string{"run", "-d", "--name", name, "-v", hostDir + ":/workspace"}, extraArgs...)
	args = append(args, image, "sleep", "600")
	out, err := exec.CommandContext(ctx, d.bin(), args...).CombinedOutput()
	if err != nil {
		t.Skipf("cannot start a %s container (image pull may need credentials): %v: %s", image, err, out)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		exec.CommandContext(c, d.bin(), "rm", "-f", name).Run() //nolint:errcheck
	})
	return name
}

// TestLiveAttachServesASessionOnAlpine is the end-to-end one, on a musl image
// on purpose: the whole binary-distribution plan rests on a CGO_ENABLED=0 build
// running on any base image, and Alpine is where a dynamically linked one would
// fail.
func TestLiveAttachServesASessionOnAlpine(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "a.txt"), []byte("one\ntwo\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	name := startLiveContainer(t, d, "alpine:latest", hostDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	t.Logf("container reports %s", goarch)

	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s — run `make build-container-server`", goarch)
	}

	stream, err := Attach(ctx, d, name, "/workspace", AttachOptions{
		Locate: func(string) (string, error) { return local, nil },
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream over docker exec: %v", err)
	}

	bufID, content, version, _, generation, err := r.OpenFile(ctx, "/workspace/a.txt")
	if err != nil {
		t.Fatalf("OpenFile in the container: %v", err)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("content = %q, want the file's — a TTY on the exec would corrupt exactly this", content)
	}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The bind mount means the host sees what the container wrote.
	saved, err := os.ReadFile(filepath.Join(hostDir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "one!\ntwo\n" {
		t.Errorf("file = %q, want %q", string(saved), "one!\ntwo\n")
	}

	stream.Close() //nolint:errcheck
}

// TestLiveBinaryIsReusedAcrossAttaches: the second window onto a container must
// not rewrite 5 MB.
//
// Asserted on the placed file's timestamp rather than on whether the local
// binary was looked up. Content-addressed placement means the local binary is
// always read — its hash is what names the destination — so "was locate
// called?" stopped being the question. Whether anything was *written* still is.
func TestLiveBinaryIsReusedAcrossAttaches(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	name := startLiveContainer(t, d, "alpine:latest", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}
	hash, err := hashFile(local)
	if err != nil {
		t.Fatal(err)
	}
	remote := ServerPath(goarch, hash)
	opts := AttachOptions{Locate: func(string) (string, error) { return local, nil }}

	stamp := func() string {
		out, err := exec.CommandContext(ctx, d.bin(), "exec", name, "stat", "-c", "%Y %s", remote).Output()
		if err != nil {
			t.Fatalf("stat %s: %v", remote, err)
		}
		return strings.TrimSpace(string(out))
	}

	first, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	first.Close() //nolint:errcheck
	before := stamp()

	// A second later, so a rewrite would move the timestamp visibly.
	time.Sleep(1100 * time.Millisecond)
	second, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	second.Close() //nolint:errcheck

	if after := stamp(); after != before {
		t.Errorf("placed binary changed from %q to %q; it was copied again", before, after)
	}
}

// TestLiveDevcontainerUpAndAttach is bite 5 end to end: the CLI builds and
// starts a container from a real devcontainer.json, and indigo connects a
// server inside it.
//
// It covers what the fakes could not — that `up` really returns the four facts
// used here, that an off-PATH docker is reached (the CLI spawns it itself, so
// it has to be told via --docker-path *and* given it on PATH for its credential
// helpers), and that remoteUser is honoured.
func TestLiveDevcontainerUpAndAttach(t *testing.T) {
	d := liveRuntime(t)
	cli := CLI{}
	if !cli.Available() {
		t.Skip("devcontainer CLI not installed")
	}
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{
  "name": "indigo live test",
  "image": "alpine:latest",
  "overrideCommand": true
}`
	if err := os.WriteFile(filepath.Join(ws, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello\nworld\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	res, err := cli.Up(ctx, ws)
	if err != nil {
		t.Fatalf("devcontainer up: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		exec.CommandContext(c, d.bin(), "rm", "-f", res.ContainerID).Run() //nolint:errcheck
	})
	t.Logf("containerId=%s remoteUser=%s remoteWorkspaceFolder=%s",
		res.ContainerID, res.RemoteUser, res.RemoteWorkspaceFolder)

	if res.ContainerID == "" || res.RemoteWorkspaceFolder == "" {
		t.Fatalf("up returned %+v, want a container and a workspace folder", res)
	}

	goarch, err := d.Arch(ctx, res.ContainerID)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s — run `make build-container-server`", goarch)
	}

	rt := Docker{Command: d.Command, User: res.RemoteUser}
	stream, err := Attach(ctx, rt, res.ContainerID, res.RemoteWorkspaceFolder, AttachOptions{
		Locate: func(string) (string, error) { return local, nil },
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}

	// The workspace folder the CLI reported is the one to open files under.
	target := res.RemoteWorkspaceFolder + "/a.txt"
	bufID, content, version, _, generation, err := r.OpenFile(ctx, target)
	if err != nil {
		t.Fatalf("OpenFile(%s): %v", target, err)
	}
	if content != "hello\nworld\n" {
		t.Fatalf("content = %q, want the file's", content)
	}

	op := document.Op{Type: document.OpInsert, InsertLine: 1, InsertCol: 5, InsertText: "!"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}

	saved, err := os.ReadFile(filepath.Join(ws, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "hello\nworld!\n" {
		t.Errorf("file = %q, want %q", string(saved), "hello\nworld!\n")
	}
	stream.Close() //nolint:errcheck
}

// TestLiveReadConfigurationReadsIndigoCustomizations closes the last gap that
// was written from documentation rather than observation.
//
// The two blocks the CLI prints carry the same customizations in different
// shapes — an object under "configuration", an array under
// "mergedConfiguration" — and modelling both as objects made the whole line
// fail to unmarshal, so serverPath was silently never read. A hand-written
// fixture could not catch that, because it had the shape the code expected.
func TestLiveReadConfigurationReadsIndigoCustomizations(t *testing.T) {
	liveRuntime(t) // skips unless a runtime answers; read-configuration needs one
	cli := CLI{}
	if !cli.Available() {
		t.Skip("devcontainer CLI not installed")
	}

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{
  "name": "indigo customizations probe",
  "image": "alpine:latest",
  "overrideCommand": true,
  "customizations": {
    "vscode": { "extensions": ["golang.go"] },
    "indigo": { "serverPath": "/usr/local/bin/indigo-server" }
  }
}`
	if err := os.WriteFile(filepath.Join(ws, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := cli.ReadConfiguration(ctx, ws)
	if err != nil {
		t.Fatalf("ReadConfiguration: %v", err)
	}
	if got := cfg.Customizations.Indigo.ServerPath; got != "/usr/local/bin/indigo-server" {
		t.Errorf("serverPath = %q, want the one in devcontainer.json", got)
	}
}

// TestLiveAttachAsNonRootUser covers the interaction the Alpine test could not:
// the server binary is copied in as root by `docker cp`, then executed as
// devcontainer.json's remoteUser, and has to be able to read and write the
// workspace as that user.
//
// The workspace here is *inside* the container rather than bind-mounted, which
// is deliberate. On Docker Desktop for macOS a bind mount synthesizes
// ownership: chown has no effect and even a file written by a non-root user
// reports as owned by root. An earlier version of this test asserted ownership
// on a bind mount and failed, which looked like an indigo bug and was not —
// checked directly before believing it. Ownership only means anything on the
// container's own filesystem.
func TestLiveAttachAsNonRootUser(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	name := startLiveContainer(t, d, "alpine:latest", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// A user that exists in the base image, and a workspace on the container's
	// own filesystem that belongs to them.
	setup := exec.CommandContext(ctx, d.bin(), "exec", name, "sh", "-c",
		"adduser -D -u 1000 dev && mkdir -p /home/dev/ws && "+
			"printf 'one\ntwo\n' > /home/dev/ws/a.txt && chown -R dev /home/dev/ws")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Skipf("cannot set up a non-root user in the image: %v: %s", err, out)
	}

	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}

	// The whole point: attach as the non-root user.
	rt := Docker{Command: d.Command, User: "dev"}
	stream, err := Attach(ctx, rt, name, "/home/dev/ws", AttachOptions{
		Locate: func(string) (string, error) { return local, nil },
	})
	if err != nil {
		t.Fatalf("Attach as a non-root user: %v", err)
	}

	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	bufID, content, version, _, generation, err := r.OpenFile(ctx, "/home/dev/ws/a.txt")
	if err != nil {
		t.Fatalf("OpenFile as a non-root user: %v", err)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("content = %q", content)
	}
	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save as a non-root user: %v", err)
	}

	// Saving is atomic — a temp file, then a rename — so this also checks the
	// replacement inherits the right owner rather than becoming root's.
	out, err := exec.CommandContext(ctx, d.bin(), "exec", name,
		"sh", "-c", "stat -c %U /home/dev/ws/a.txt; cat /home/dev/ws/a.txt").Output()
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if lines[0] != "dev" {
		t.Errorf("saved file is owned by %q, want dev — the developer could not edit it elsewhere", lines[0])
	}
	if len(lines) < 2 || lines[1] != "one!\ntwo" {
		t.Errorf("file content = %q, want the edit applied", lines[1:])
	}
	stream.Close() //nolint:errcheck
}

// TestExecSurvivesTheSetupContextBeingCancelled reproduces the shape of a bug
// that reached a real run: the session died milliseconds after connecting.
//
// `connect()` does what any Go code does — takes a context with a timeout for
// the work it is about to do and defers the cancel. But the stream it returns
// is the editor's connection for the whole session, and passing that context to
// exec.CommandContext meant the deferred cancel killed `docker exec` the
// instant the setup function returned.
//
// The existing live tests could not catch it because their own `defer cancel()`
// sits at the end of the test function, outliving the stream. This one cancels
// *immediately*, the way production does, and then uses the stream.
func TestExecSurvivesTheSetupContextBeingCancelled(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "a.txt"), []byte("one\ntwo\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	name := startLiveContainer(t, d, "alpine:latest", hostDir)

	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 2*time.Minute)
	goarch, err := d.Arch(setupCtx, name)
	if err != nil {
		cancelSetup()
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		cancelSetup()
		t.Skipf("no server binary for %s", goarch)
	}

	stream, err := Attach(setupCtx, d, name, "/workspace", AttachOptions{
		Locate: func(string) (string, error) { return local, nil },
	})
	if err != nil {
		cancelSetup()
		t.Fatalf("Attach: %v", err)
	}
	// Exactly what connect()'s `defer cancel()` does when it returns.
	cancelSetup()

	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream after the setup context was cancelled: %v", err)
	}

	// A real round trip *after* the cancel. Dial alone is not enough: the
	// failure showed up as the handshake completing and the stream dying a
	// couple of milliseconds later.
	useCtx, cancelUse := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelUse()
	bufID, content, version, _, generation, err := r.OpenFile(useCtx, "/workspace/a.txt")
	if err != nil {
		t.Fatalf("OpenFile after the setup context was cancelled: %v — "+
			"the container process is tied to the setup context again", err)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("content = %q", content)
	}
	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	if _, err := r.ApplyOp(useCtx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp after the setup context was cancelled: %v", err)
	}
	if err := r.Save(useCtx, bufID); err != nil {
		t.Fatalf("Save after the setup context was cancelled: %v", err)
	}
	stream.Close() //nolint:errcheck
}

// TestLiveTwoAttachesShareOneServer is the reason cmd/indigo-server is a bridge
// rather than a server on stdio.
//
// One `docker exec` is one process, so serving the connection directly gave
// every window its own server and its own buffer table — two windows editing
// one file would each hold a private copy and write over each other, with none
// of the operational transform that makes concurrent editing converge. Measured
// before the fix: two attaches, two servers.
//
// On the host this never arises, because one server per workspace listens on a
// socket. This asserts the container behaves the same way.
func TestLiveTwoAttachesShareOneServer(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "a.txt"), []byte("one\ntwo\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	name := startLiveContainer(t, d, "alpine:latest", hostDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}
	opts := AttachOptions{Locate: func(string) (string, error) { return local, nil }}

	first, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	defer first.Close() //nolint:errcheck
	r1, err := client.DialStream(first)
	if err != nil {
		t.Fatalf("first DialStream: %v", err)
	}

	second, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	defer second.Close() //nolint:errcheck
	r2, err := client.DialStream(second)
	if err != nil {
		t.Fatalf("second DialStream: %v", err)
	}

	// Exactly one daemon, however many windows are attached.
	out, err := exec.CommandContext(ctx, d.bin(), "exec", name, "sh", "-c",
		"ps -eo args | grep '[-]-daemon /workspace' | wc -l").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "1" {
		t.Errorf("%s server daemons running, want 1 — each window has its own buffers", got)
	}

	// The behavioural half, which is what actually matters: both windows get
	// the *same* buffer for the same file. A per-window server would hand out
	// two different ids for two independent copies.
	id1, _, _, _, _, err := r1.OpenFile(ctx, "/workspace/a.txt")
	if err != nil {
		t.Fatalf("first OpenFile: %v", err)
	}
	id2, _, version2, _, generation2, err := r2.OpenFile(ctx, "/workspace/a.txt")
	if err != nil {
		t.Fatalf("second OpenFile: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("buffer ids %d and %d differ — the two windows are not sharing a buffer", id1, id2)
	}

	// And an edit in one is visible to the other, which is the whole point of
	// sharing a server.
	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	if _, err := r2.ApplyOp(ctx, id2, op, generation2, version2); err != nil {
		t.Fatalf("ApplyOp from the second window: %v", err)
	}
	ops, _, _, _, _, err := r1.GetUpdates(ctx, id1, version2)
	if err != nil {
		t.Fatalf("GetUpdates in the first window: %v", err)
	}
	if len(ops) == 0 {
		t.Error("the first window saw no ops from the second; they are not sharing a server")
	}
}

// TestLiveDaemonExitsWhenTheLastWindowLeaves guards the other half of the
// bridge's lifetime contract.
//
// Sharing a daemon across windows is only correct if it also *goes away* when
// the last one closes — a server that outlives every window leaves a process
// and its buffers sitting in the container indefinitely, which on a long-lived
// dev container accumulates. It must also survive the *first* window closing
// while a second is still attached, which is the case the old stdio topology
// could not get wrong because there was nothing shared to get wrong.
func TestLiveDaemonExitsWhenTheLastWindowLeaves(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	name := startLiveContainer(t, d, "alpine:latest", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}
	opts := AttachOptions{Locate: func(string) (string, error) { return local, nil }}

	daemons := func() string {
		out, err := exec.CommandContext(ctx, d.bin(), "exec", name, "sh", "-c",
			"ps -eo args | grep '[-]-daemon /workspace' | wc -l").Output()
		if err != nil {
			t.Fatalf("ps: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	first, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	r1, err := client.DialStream(first)
	if err != nil {
		t.Fatalf("first DialStream: %v", err)
	}
	second, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	r2, err := client.DialStream(second)
	if err != nil {
		t.Fatalf("second DialStream: %v", err)
	}
	// Both must have connected, or "the daemon is still running" below would
	// be true for the wrong reason.
	if _, _, _, _, _, err := r1.OpenFile(ctx, "/workspace/x.txt"); err != nil {
		t.Fatalf("first OpenFile: %v", err)
	}
	if _, _, _, _, _, err := r2.OpenFile(ctx, "/workspace/x.txt"); err != nil {
		t.Fatalf("second OpenFile: %v", err)
	}

	// One window closes: the daemon must stay, because the other is still in it.
	first.Close() //nolint:errcheck
	time.Sleep(500 * time.Millisecond)
	if got := daemons(); got != "1" {
		t.Fatalf("%s daemons after one of two windows closed, want 1 — "+
			"closing one window took the other's server with it", got)
	}

	// The last window closes: the daemon must go.
	second.Close() //nolint:errcheck
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if daemons() == "0" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("the daemon outlived its last window; a long-lived container would accumulate them")
}

// TestLiveServerRunningTracksTheDaemon checks the probe the shutdown decision
// rests on. Stopping a container while another window is editing in it would be
// far worse than leaving one running, so this answer has to be right in both
// directions.
func TestLiveServerRunningTracksTheDaemon(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	name := startLiveContainer(t, d, "alpine:latest", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}
	opts := AttachOptions{Locate: func(string) (string, error) { return local, nil }}

	// Nothing attached yet.
	if running, err := ServerRunning(ctx, d, name, "/workspace"); err != nil || running {
		t.Fatalf("ServerRunning before any attach = (%v, %v), want (false, nil)", running, err)
	}

	stream, err := Attach(ctx, d, name, "/workspace", opts)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	if _, _, _, _, _, err := r.OpenFile(ctx, "/workspace/x.txt"); err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if running, err := ServerRunning(ctx, d, name, "/workspace"); err != nil || !running {
		t.Fatalf("ServerRunning with a window attached = (%v, %v), want (true, nil)", running, err)
	}

	// And it reports false again once the window goes, which is what releases
	// the container to be stopped.
	stream.Close() //nolint:errcheck
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		running, err := ServerRunning(ctx, d, name, "/workspace")
		if err != nil {
			t.Fatalf("ServerRunning: %v", err)
		}
		if !running {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("ServerRunning still reports true after the last window closed; the container would never be stopped")
}

// TestLiveDevcontainerStopActuallyStops covers the other end: shutting the
// container down when indigo is finished with it.
//
// Through the runtime, not the devcontainer CLI — it has no stop command, which
// only reading `devcontainer --help` on the real thing revealed.
func TestLiveDevcontainerStopActuallyStops(t *testing.T) {
	d := liveRuntime(t)
	cli := CLI{}
	if !cli.Available() {
		t.Skip("devcontainer CLI not installed")
	}

	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".devcontainer"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"name":"stop test","image":"alpine:latest","overrideCommand":true}`
	if err := os.WriteFile(filepath.Join(ws, ".devcontainer", "devcontainer.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := cli.Up(ctx, ws)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		exec.CommandContext(c, d.bin(), "rm", "-f", res.ContainerID).Run() //nolint:errcheck
	})

	state := func() string {
		out, _ := exec.CommandContext(ctx, d.bin(), "inspect", "-f", "{{.State.Running}}", res.ContainerID).Output()
		return strings.TrimSpace(string(out))
	}
	if state() != "true" {
		t.Fatalf("container is not running after up: %q", state())
	}

	if err := d.Stop(ctx, res.ContainerID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if state() == "false" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("container still running after Stop: %q", state())
}

// TestLiveOtherWindowsAttachedIsInstantAndCorrect drives the decision that
// replaced a five-second pause on every quit.
//
// The pause was there because "is the daemon still running?" cannot distinguish
// another window from a daemon that has not finished shutting down, so the only
// safe answer was to wait. Counting bridges, with each window able to exclude
// its own, answers outright — and has to be right in both directions, because
// a false "nobody else" stops a container under a live window.
func TestLiveOtherWindowsAttachedIsInstantAndCorrect(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	name := startLiveContainer(t, d, "alpine:latest", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	goarch, err := d.Arch(ctx, name)
	if err != nil {
		t.Fatalf("Arch: %v", err)
	}
	local := filepath.Join("..", "..", "dist", "indigo-server-linux-"+goarch)
	if _, err := os.Stat(local); err != nil {
		t.Skipf("no server binary for %s", goarch)
	}
	locate := func(string) (string, error) { return local, nil }

	first, err := Attach(ctx, d, name, "/workspace", AttachOptions{Locate: locate, ClientToken: "tokenA"})
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if _, err := client.DialStream(first); err != nil {
		t.Fatalf("first DialStream: %v", err)
	}

	// One window: it sees nobody else, so it would stop the container.
	others, err := OtherWindowsAttached(ctx, d, name, "/workspace", "tokenA")
	if err != nil {
		t.Fatalf("OtherWindowsAttached: %v", err)
	}
	if others != 0 {
		t.Errorf("others = %d with one window attached, want 0", others)
	}

	second, err := Attach(ctx, d, name, "/workspace", AttachOptions{Locate: locate, ClientToken: "tokenB"})
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	if _, err := client.DialStream(second); err != nil {
		t.Fatalf("second DialStream: %v", err)
	}

	// Each window sees exactly the other, and neither counts itself.
	start := time.Now()
	for token, want := range map[string]int{"tokenA": 1, "tokenB": 1} {
		got, err := OtherWindowsAttached(ctx, d, name, "/workspace", token)
		if err != nil {
			t.Fatalf("OtherWindowsAttached(%s): %v", token, err)
		}
		if got != want {
			t.Errorf("window %s sees %d others, want %d", token, got, want)
		}
	}
	// "Instant" is the point — the old answer took five seconds by construction.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("two probes took %v; this is meant to be a question, not a wait", elapsed)
	}

	// The second window leaves: the first must now see nobody, promptly.
	second.Close() //nolint:errcheck
	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := OtherWindowsAttached(ctx, d, name, "/workspace", "tokenA")
		if err != nil {
			t.Fatalf("OtherWindowsAttached: %v", err)
		}
		if got == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still sees %d others after the second window closed", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	first.Close() //nolint:errcheck
}
