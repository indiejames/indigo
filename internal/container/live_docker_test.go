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
// not re-copy 5 MB.
func TestLiveBinaryIsReusedAcrossAttaches(t *testing.T) {
	d := liveRuntime(t)
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostDir := t.TempDir()
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

	first, err := Attach(ctx, d, name, "/workspace", AttachOptions{
		Locate: func(string) (string, error) { return local, nil },
	})
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	first.Close() //nolint:errcheck

	second, err := Attach(ctx, d, name, "/workspace", AttachOptions{
		Locate: func(string) (string, error) {
			t.Error("copied the server again although the container already had it")
			return local, nil
		},
	})
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	second.Close() //nolint:errcheck
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
