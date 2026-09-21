package container

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/procutil"
)

// localRuntime is a Runtime that runs commands on this machine instead of
// inside a container.
//
// It exists because the development machine for this work has no container
// engine, and the in-memory fake above proves only that Attach calls things in
// the right order. This proves the rest: that a real server binary, started as
// a child process and spoken to over its stdio, actually serves a client — the
// whole stack bar the `docker` argv, which TestExecArgsNeverAllocateATTY pins
// separately.
//
// It deliberately shares procStream with the Docker implementation, so the pipe
// plumbing and the Close-ends-the-process semantics under test here are the
// same code that runs against a real container.
type localRuntime struct {
	// root stands in for the container's filesystem: every path Attach uses is
	// resolved beneath it, so the test never writes to the real /tmp and two
	// runs cannot collide. It is also a small preview of the path translation
	// bite 4 has to do for real.
	root string
}

func (l localRuntime) path(p string) string { return filepath.Join(l.root, p) }

func (localRuntime) Arch(context.Context, string) (string, error) {
	return runtime.GOARCH, nil
}

func (l localRuntime) FileExists(_ context.Context, _, path string) (bool, error) {
	info, err := os.Stat(l.path(path))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return !info.IsDir(), nil
}

func (l localRuntime) CopyIn(_ context.Context, _, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return err
	}
	target := l.path(remotePath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o755)
}

func (l localRuntime) Exec(ctx context.Context, _ string, argv []string) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, l.path(argv[0]), argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	// Same process group treatment as Docker.Exec, so this stands in for it
	// faithfully — a server killed without its plugin children would leave them
	// holding the test binary's stdio.
	procutil.SetPgid(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procStream{cmd: cmd, in: stdin, out: stdout}, nil
}

// buildServer compiles cmd/indigo-server for this machine, the way
// internal/plugin's integration test compiles its fixture plugin.
func buildServer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "indigo-server")
	cmd := exec.Command("go", "build", "-o", out, "../../cmd/indigo-server")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot build the server binary here: %v", err)
	}
	return out
}

// TestAttachServesARealClientOverASpawnedServer is the end-to-end one: Attach
// places a binary and starts it, and the stream it hands back carries a full
// session — open, edit, save to disk — after which closing the stream must end
// the process, or every window that closes would leave a server behind inside
// the container.
func TestAttachServesARealClientOverASpawnedServer(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	built := buildServer(t)

	workDir := t.TempDir()
	path := filepath.Join(workDir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rt := localRuntime{root: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream, err := Attach(ctx, rt, "ignored", workDir, AttachOptions{Locate: func(string) (string, error) {
		return built, nil
	}})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// The binary landed where Attach says it puts it.
	if _, err := os.Stat(rt.path(ServerPath(runtime.GOARCH))); err != nil {
		t.Fatalf("Attach did not place the server: %v", err)
	}

	r, err := client.DialStream(stream)
	if err != nil {
		t.Fatalf("DialStream onto a spawned server: %v", err)
	}

	bufID, content, version, _, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("content = %q", content)
	}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "one!\ntwo\n" {
		t.Errorf("file on disk = %q, want %q", string(saved), "one!\ntwo\n")
	}

	stream.Close() //nolint:errcheck
	if ps := stream.(*procStream); ps.cmd.ProcessState == nil {
		t.Error("the server process was not reaped when the stream closed")
	}
}

// TestAttachSkipsTheCopyAgainstRealFiles is the fake's skip-the-copy assertion
// against a real filesystem: the second window onto a container must not
// rewrite the binary.
func TestAttachSkipsTheCopyAgainstRealFiles(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	built := buildServer(t)
	rt := localRuntime{root: t.TempDir()}
	workDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	first, err := Attach(ctx, rt, "ignored", workDir, AttachOptions{Locate: func(string) (string, error) { return built, nil }})
	if err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	first.Close() //nolint:errcheck

	second, err := Attach(ctx, rt, "ignored", workDir, AttachOptions{Locate: func(string) (string, error) {
		t.Error("looked for a local binary although one was already placed")
		return built, nil
	}})
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	second.Close() //nolint:errcheck
}
