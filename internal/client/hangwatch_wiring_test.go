package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/hangdetect"
)

// TestDialWatchesOutgoingCalls is the dispatch half for the client side, and
// doubles as the end-to-end check that wrapping the bootstrap capability has
// not changed what a real round trip does.
//
// The structural assertion is the point: an unwrapped capability serves every
// call exactly as well, so the only thing that would ever notice it going
// missing is a stall nobody is watching for any more.
func TestDialWatchesOutgoingCalls(t *testing.T) {
	_, sock := startOTServer(t)
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

// TestArmedDetectorStaysQuietDuringNormalEditing is the cost side of the
// bargain. A stall report is only worth reading if its presence means
// something, and a detector that fires during ordinary work is one that gets
// ignored — at which point the real stall it was built for is ignored too.
//
// Everything here is a real round trip against a real server: connect, open,
// edit, poll, save, close.
func TestArmedDetectorStaysQuietDuringNormalEditing(t *testing.T) {
	_, sock := startOTServer(t)

	hangdetect.Start()
	t.Cleanup(hangdetect.Stop)
	start := time.Now()

	r, err := Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})

	path := filepath.Join(t.TempDir(), "a.txt") // see above: a .go starts a real language server
	if err := os.WriteFile(path, []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, _, version, _, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	op := document.Op{Type: document.OpInsert, InsertLine: 1, InsertCol: 0, InsertText: "// hi\n"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if _, _, _, _, _, err := r.GetUpdates(ctx, bufID, version); err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Let a few scan intervals pass, so a detector that was going to fire has
	// had the chance to.
	time.Sleep(3 * time.Second)

	entries, err := debuglog.Read(debuglog.ReadOptions{Since: start.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("debuglog.Read: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Raw, hangdetect.Prefix) {
			t.Errorf("ordinary editing produced a stall report: %s", e.Raw)
		}
	}
}
