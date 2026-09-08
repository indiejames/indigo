package agenttools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/server"
)

// TestWorkspaceRootMatchesIndigosRule covers the resolution that lets a single
// user-level MCP registration work everywhere: the workspace is the nearest
// ancestor with a .git, **symlinks resolved**, so running the agent from a
// subdirectory reaches the same indigo server the editor would use.
//
// Disagreeing with cmd/indigo's resolvePath+gitRoot on either count silently
// starts a second server for one repo, with its own buffers — server.SocketPath
// is derived from this string, so two spellings of one directory are two
// workspaces. The symlink half is not hypothetical on macOS, where TMPDIR
// itself lives under /var -> /private/var.
//
// want() resolves with filepath.EvalSymlinks rather than the package's own
// helper, so this asserts the actual property instead of comparing the
// implementation to itself.
func TestWorkspaceRootMatchesIndigosRule(t *testing.T) {
	want := func(t *testing.T, p string) string {
		t.Helper()
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("EvalSymlinks(%q): %v", p, err)
		}
		return resolved
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("from the root", func(t *testing.T) {
		if got, w := workspaceRoot(root), want(t, root); got != w {
			t.Errorf("workspaceRoot(%q) = %q, want %q", root, got, w)
		}
	})

	t.Run("from a subdirectory", func(t *testing.T) {
		if got, w := workspaceRoot(deep), want(t, root); got != w {
			t.Errorf("workspaceRoot(%q) = %q, want the repo root %q — a subdirectory would "+
				"otherwise get its own server and its own buffers", deep, got, w)
		}
	})

	t.Run("outside any repo falls back to the directory itself", func(t *testing.T) {
		bare := t.TempDir()
		if got, w := workspaceRoot(bare), want(t, bare); got != w {
			t.Errorf("workspaceRoot(%q) = %q, want %q — the fallback must be resolved too, "+
				"or a non-repo directory keeps the unresolved spelling", bare, got, w)
		}
	})

	// An explicit symlink, so the property holds regardless of whether the
	// platform's TMPDIR happens to be symlinked.
	t.Run("reached through a symlink", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link-to-repo")
		if err := os.Symlink(root, link); err != nil {
			t.Skipf("cannot create a symlink here: %v", err)
		}
		if got, w := workspaceRoot(link), want(t, root); got != w {
			t.Errorf("workspaceRoot(%q) = %q, want %q — an unresolved workspace hashes to a "+
				"different socket than the editor's, and a linter spawned there reports a "+
				"cwd that disagrees with it", link, got, w)
		}
		// And from a subdirectory reached through the same link.
		via := filepath.Join(link, "a", "b", "c")
		if got, w := workspaceRoot(via), want(t, root); got != w {
			t.Errorf("workspaceRoot(%q) = %q, want %q", via, got, w)
		}
	})
}

// TestStandaloneApprovalDoesNotBlock is a regression guard for a deadlock the
// standalone mode would otherwise hit: requestEditApproval emits a permission
// request to the TUI program and then blocks on the reply channel. With no
// TUI, emit is a no-op and nothing ever replies, so an edit would hang until
// the tool timeout with no indication why.
//
// Standalone stands the plugin's own gate down and leaves approval to the MCP
// client (the claude CLI prompts before calling a tool), which is both the
// only workable option and the same model every other MCP server uses.
func TestStandaloneApprovalDoesNotBlock(t *testing.T) {
	// The real thing standalone runs with, not a lookalike.
	ap := standaloneApprover()

	done := make(chan bool, 1)
	go func() {
		done <- requestEditApproval(ap, EditRequest{File: "a.txt", Reason: "test"})
	}()

	select {
	case ok := <-done:
		if !ok {
			t.Error("approval returned false; standalone mode must not reject its own edits")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("requestEditApproval blocked with no TUI attached — the standalone " +
			"programLink must not wait for a reply that can never come")
	}
}

// TestMCPConnRedialsAfterServerExit is a regression test for the failure that
// made the edit tools unusable while the read tools looked fine.
//
// RunStandalone used to dial once and capture the handle for the life of the
// process. An indigo server exits when its last client disconnects, and this
// process outlives any editor window, so that handle regularly went dead
// mid-session — after which every call returned "rpc: connection closed"
// forever. The visible symptoms were asymmetric and misleading: apply_edits
// failed, so the agent fell back to filesystem tools, while read_file quietly
// served on-disk bytes and appeared to work.
//
// The server here runs in-process and is kept alive by a second client, so the
// test needs no `indigo` on PATH (CI has none) and exercises the part that was
// actually broken: noticing a dead connection and replacing it. Restarting a
// server that has genuinely exited is startIndigoServer's job, covered
// separately below.
func TestMCPConnRedialsAfterServerExit(t *testing.T) {
	dir := t.TempDir()
	sock := server.SocketPath(dir)

	srv, err := server.New(dir)
	if err != nil {
		t.Fatalf("start an in-process server: %v", err)
	}
	done := make(chan struct{})
	go func() { srv.Wait(); close(done) }()
	waitUntil(t, func() bool { return server.IsRunning(sock) }, "the server to accept connections")

	// Holding a second client keeps the server up when the connection under
	// test goes away — otherwise the last disconnect shuts it down and get()
	// would need to spawn a replacement.
	keepalive, err := client.Dial(sock)
	if err != nil {
		t.Fatalf("keepalive dial: %v", err)
	}
	t.Cleanup(func() {
		keepalive.Disconnect(context.Background()) //nolint:errcheck
		<-done
	})

	c := &mcpConn{workDir: dir, sock: sock}

	first, err := c.get()
	if err != nil {
		t.Fatalf("initial connect: %v", err)
	}
	if !first.Alive() {
		t.Fatal("a freshly dialed connection reports itself dead")
	}
	// get() must reuse a live connection rather than dialing per call.
	if again, err := c.get(); err != nil || again != first {
		t.Errorf("live connection was not reused: reused=%v, err=%v", again == first, err)
	}

	// Drop the connection the way a server exit does.
	if err := first.Disconnect(context.Background()); err != nil {
		t.Logf("disconnect returned %v (the connection is closed either way)", err)
	}
	waitUntil(t, func() bool { return !first.Alive() }, "connection to report itself closed")

	second, err := c.get()
	if err != nil {
		t.Fatalf("redial after the connection died: %v", err)
	}
	if second == first {
		t.Error("get() handed back the dead connection; every later tool call would fail " +
			"with \"rpc: connection closed\"")
	}
	if !second.Alive() {
		t.Error("redialed connection is not alive")
	}
	second.Disconnect(context.Background()) //nolint:errcheck
}

// TestMCPConnStartsServerWhenNoneRunning covers get()'s other branch: no server
// at all, so one has to be spawned. That needs the real `indigo` binary, which
// a checkout does not have until `make install`, so it is skipped rather than
// failed when absent — the branch above is the one carrying the regression.
func TestMCPConnStartsServerWhenNoneRunning(t *testing.T) {
	if _, err := exec.LookPath("indigo"); err != nil {
		t.Skip("no indigo on PATH; this branch spawns the real binary")
	}
	dir := t.TempDir()
	c := &mcpConn{workDir: dir, sock: server.SocketPath(dir)}

	rpc, err := c.get()
	if err != nil {
		t.Fatalf("get() with no server running: %v", err)
	}
	if !rpc.Alive() {
		t.Error("connection to the spawned server is not alive")
	}
	rpc.Disconnect(context.Background()) //nolint:errcheck
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
