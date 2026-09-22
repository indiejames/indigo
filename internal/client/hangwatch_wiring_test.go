package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/hangdetect"
)

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
