package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"capnproto.org/go/capnp/v3/rpc"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/proto"
)

// startTestServer brings up a real listener running the real serve() loop, so
// a test can end a connection the way a crashed client does — by hanging up
// rather than by calling Disconnect. Built by hand rather than via New() to
// keep the user's config, language servers, and workspace scans out of it.
func startTestServer(t *testing.T, workDir string) *Server {
	t.Helper()
	// Not t.TempDir(): its name embeds the test's, and a unix socket path is
	// capped near 104 bytes on macOS — long test names push it over and the
	// bind fails with a bare "invalid argument".
	sockDir, err := os.MkdirTemp("", "indigo-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) }) //nolint:errcheck
	sockPath := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RecoveryMaxBytes: 1 << 20}
	srv := &Server{
		socketPath: sockPath,
		listener:   ln,
		done:       make(chan struct{}),
		svc: &editorService{
			cfg:         cfg,
			buffers:     map[uint32]*bufferEntry{},
			clientMap:   map[uint64]*clientEntry{},
			fmtMgr:      format.NewManager(nil, cfg, workDir),
			lspMgr:      lsp.NewManager(workDir, nil),
			lintMgr:     &lint.Manager{},
			pluginMgr:   &plugin.Manager{},
			dirWatches:  make(map[string]int),
			savingPaths: make(map[string]time.Time),
			recDir:      t.TempDir(),
			statusBar:   newStatusBarRegistry(),
		},
	}
	go srv.serve()
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck
	return srv
}

// dialTestServer opens one client connection and returns its EditorService
// along with a hangUp func that ends the connection without ever sending
// Disconnect — a killed window, a dropped ssh session, an agent tool whose
// context expired.
func dialTestServer(t *testing.T, srv *Server) (proto.EditorService, func()) {
	t.Helper()
	c, err := net.Dial("unix", srv.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	conn := rpc.NewConn(rpc.NewStreamTransport(c), nil)
	cl := proto.EditorService(conn.Bootstrap(context.Background()))
	hangUp := func() { conn.Close() } //nolint:errcheck
	t.Cleanup(hangUp)
	return cl, hangUp
}

// connectClient performs the Connect handshake and returns the client id.
func connectClient(t *testing.T, cl proto.EditorService) uint64 {
	t.Helper()
	fut, rel := cl.Connect(context.Background(), func(p proto.EditorService_connect_Params) error {
		return nil // no callback: this test never exercises server pushes
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return res.ClientId()
}

// TestDroppedConnectionReleasesItsBuffers is a regression test for a user
// report of indigo serving the contents of a file that had since been changed
// on disk — and going on serving them after the file was closed and reopened,
// with only a full restart of the editor recovering.
//
// Disconnect is an explicit RPC, so only an orderly quit sends it. A client
// that dies any other way used to stay registered as a client of every buffer
// it had open, for the life of the server. Since CloseBuffer frees a buffer
// only once its client set empties, that corpse pinned the buffer forever, and
// OpenFile's attach path kept answering with its in-memory content instead of
// reading the file. Nothing could dislodge it: the external-change watcher's
// reload is CloseBuffer + OpenFile, which re-attached to the same pinned
// entry, and so did closing and reopening the file by hand.
func TestDroppedConnectionReleasesItsBuffers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.ts")
	const beforeExternalEdit = "const f = function() {}\n"
	const afterExternalEdit = "const f = function () {}\n"
	if err := os.WriteFile(path, []byte(beforeExternalEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startTestServer(t, dir)

	// Two windows on the same workspace, the way the report describes. The
	// survivor matters: it keeps the server alive past the first one's death,
	// which is the whole window in which the leak is observable.
	dying, hangUp := dialTestServer(t, srv)
	dyingID := connectClient(t, dying)
	survivor, _ := dialTestServer(t, srv)
	survivorID := connectClient(t, survivor)

	if _, content, err := openFileFor(t, dying, dyingID, path); err != nil {
		t.Fatalf("OpenFile from the dying client: %v", err)
	} else if content != beforeExternalEdit {
		t.Fatalf("OpenFile content = %q, want %q", content, beforeExternalEdit)
	}

	// The window dies. No Disconnect is sent — that is the entire point.
	hangUp()
	waitFor(t, "the server to release the dropped connection's buffer", func() bool {
		srv.svc.mu.Lock()
		defer srv.svc.mu.Unlock()
		return len(srv.svc.buffers) == 0
	})

	// Meanwhile the file is fixed in another editor.
	if err := os.WriteFile(path, []byte(afterExternalEdit), 0o644); err != nil {
		t.Fatal(err)
	}

	_, content, err := openFileFor(t, survivor, survivorID, path)
	if err != nil {
		t.Fatalf("OpenFile from the surviving client: %v", err)
	}
	if content != afterExternalEdit {
		t.Errorf("OpenFile served %q, want the file's current contents %q — a buffer pinned by "+
			"a client that never disconnected is being served instead of reading the file",
			content, afterExternalEdit)
	}
}

// TestDroppedConnectionKeepsBufferHeldByAnotherClient is the other half of the
// contract: releasing the dead client's hold must not disturb a buffer someone
// else still has open, whose in-memory content is authoritative (it may hold
// unsaved edits) and must keep being served.
func TestDroppedConnectionKeepsBufferHeldByAnotherClient(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.ts")
	const original = "const shared = 1\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startTestServer(t, dir)

	dying, hangUp := dialTestServer(t, srv)
	dyingID := connectClient(t, dying)
	survivor, _ := dialTestServer(t, srv)
	survivorID := connectClient(t, survivor)

	sharedBufID, _, err := openFileFor(t, dying, dyingID, path)
	if err != nil {
		t.Fatalf("OpenFile from the dying client: %v", err)
	}
	if _, _, err := openFileFor(t, survivor, survivorID, path); err != nil {
		t.Fatalf("OpenFile from the surviving client: %v", err)
	}

	hangUp()
	waitFor(t, "the dropped client to be removed from the shared buffer", func() bool {
		srv.svc.mu.Lock()
		defer srv.svc.mu.Unlock()
		e, ok := srv.svc.buffers[sharedBufID]
		if !ok {
			return false
		}
		_, stillThere := e.clients[dyingID]
		return !stillThere
	})

	srv.svc.mu.Lock()
	entry, ok := srv.svc.buffers[sharedBufID]
	srv.svc.mu.Unlock()
	if !ok {
		t.Fatal("buffer was freed while another client still had it open")
	}
	if _, held := entry.clients[survivorID]; !held {
		t.Error("surviving client lost its hold on the buffer")
	}
}

// TestDroppedConnectionPreservesUnsavedWork covers the difference between
// losing a client and closing a file. CloseBuffer is an explicit "I'm done
// with this", so it deletes the recovery file; a connection dying is not
// consent to discard unsaved edits, so freeing the buffer has to leave a
// recovery file behind holding its final content.
func TestDroppedConnectionPreservesUnsavedWork(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsaved.ts")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := startTestServer(t, dir)

	dying, hangUp := dialTestServer(t, srv)
	dyingID := connectClient(t, dying)
	survivor, _ := dialTestServer(t, srv)
	connectClient(t, survivor)

	bufID, _, err := openFileFor(t, dying, dyingID, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	// An unsaved edit: insert "y" at the start of line 0.
	fut, rel := dying.ApplyOps(context.Background(), func(p proto.EditorService_applyOps_Params) error {
		p.SetClientId(dyingID)
		p.SetBufferId(bufID)
		list, err := p.NewOps(1)
		if err != nil {
			return err
		}
		ins := list.At(0)
		ins.SetType(proto.EditOp_OpType_insert)
		ins.SetInsertLine(0)
		ins.SetInsertCol(0)
		return ins.SetInsertText("y")
	})
	if _, err := fut.Struct(); err != nil {
		rel()
		t.Fatalf("ApplyOps: %v", err)
	}
	rel()

	hangUp()
	recPath := recoveryFilePath(srv.svc.recDir, path)
	waitFor(t, "a recovery file holding the dropped client's unsaved work", func() bool {
		data, err := os.ReadFile(recPath)
		return err == nil && string(data) != ""
	})

	data, err := os.ReadFile(recPath)
	if err != nil {
		t.Fatalf("reading recovery file: %v", err)
	}
	if got := string(data); got != "yx\n" {
		t.Errorf("recovery file = %q, want the buffer's final unsaved content %q", got, "yx\n")
	}
}

// waitFor polls cond until it holds, failing the test with what it was waiting
// for if it never does. The teardown under test runs on the server's own
// connection goroutine, so there is nothing to synchronize on directly.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
