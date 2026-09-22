package client

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/server"
)

// TestStreamTransportRoundTrip drives a real client against a real server with
// no socket anywhere between them — the two halves are joined by an in-memory
// pipe, which is what the container runtime's exec stdio will be.
//
// This is the whole foundation of dev-container support, so it is worth being
// explicit about what it proves: that the connection works when it is handed a
// stream rather than a listener, and that the server behind it is the same
// server. The handshake, an open, an edit, a poll and a save all have to work,
// because a transport that carries the handshake and then mangles a later
// message is the failure mode worth catching.
func TestStreamTransportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostSide, containerSide := net.Pipe()

	srv, err := server.ServeStream(dir, containerSide)
	if err != nil {
		t.Fatalf("ServeStream: %v", err)
	}
	r, err := DialStream(hostSide)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	// See workspacefs_rpc_test.go: registered after the client exists, so a
	// failed dial cannot leave Wait blocking for ever.
	t.Cleanup(srv.Wait)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})

	// A .txt, not a .go: a real server starts a real language server for a file
	// it recognises, and that child outlives the test binary.
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bufID, content, version, _, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile over a stream: %v", err)
	}
	if content != "one\ntwo\n" {
		t.Fatalf("content = %q, want the file's", content)
	}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 3, InsertText: "!"}
	newVersion, err := r.ApplyOp(ctx, bufID, op, generation, version)
	if err != nil {
		t.Fatalf("ApplyOp over a stream: %v", err)
	}
	if newVersion <= version {
		t.Errorf("version did not advance: %d -> %d", version, newVersion)
	}

	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save over a stream: %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != "one!\ntwo\n" {
		t.Errorf("file on disk = %q, want %q — the edit did not survive the stream",
			string(saved), "one!\ntwo\n")
	}
}

// TestStreamServerShutsDownWhenTheStreamCloses pins the lifecycle difference
// that comes with having no listener: with one connection and no way to accept
// another, the client hanging up is the end of the server. A socket-served
// server waits for the last of several clients; this one has exactly one, and
// if it kept running the container would be left holding an orphan.
func TestStreamServerShutsDownWhenTheStreamCloses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	hostSide, containerSide := net.Pipe()
	srv, err := server.ServeStream(dir, containerSide)
	if err != nil {
		t.Fatalf("ServeStream: %v", err)
	}

	r, err := DialStream(hostSide)
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}

	// Connect is what marks the server as having had a client, which is the
	// precondition for shutting down when the last one leaves.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if _, _, _, _, _, err := r.OpenFile(ctx, filepath.Join(dir, "x.txt")); err != nil {
		cancel()
		t.Fatalf("OpenFile: %v", err)
	}
	cancel()

	hostSide.Close() //nolint:errcheck

	done := make(chan struct{})
	go func() {
		srv.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server outlived its only client; a container would be left holding it")
	}
}
