package rpcclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/indiejames/indigo/internal/server"
)

// startTestServer runs a real server for a fresh workspace and returns its
// socket path.
func startTestServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)
	return server.SocketPath(dir)
}

// TestDialWatchesOutgoingCalls is the dispatch half for the client side, and
// doubles as the end-to-end check that wrapping the bootstrap capability has
// not changed what a real round trip does.
//
// The structural assertion is the point: an unwrapped capability serves every
// call exactly as well, so the only thing that would ever notice it going
// missing is a stall nobody is watching for any more.
func TestDialWatchesOutgoingCalls(t *testing.T) {
	sock := startTestServer(t)
	r, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})

	if got := capnp.Client(r.svc).String(); !strings.Contains(got, "rpcwatch.outgoing") {
		t.Errorf("service capability = %q; outgoing calls are not being watched", got)
	}

	// And a real call still works through it, with the content it was given.
	dir := t.TempDir()
	// A .txt rather than a .go: startOTServer runs a real server, and a real
	// server starts a real language server for a file it recognises. The
	// child outlives the test binary and turns a passing run into "Test I/O
	// incomplete". Nothing here needs a language to be recognised.
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, content, _, _, _, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile through the wrapper: %v", err)
	}
	if bufID == 0 || content != "package main\n" {
		t.Errorf("OpenFile returned bufID=%d content=%q, want a real buffer holding the file", bufID, content)
	}
}
