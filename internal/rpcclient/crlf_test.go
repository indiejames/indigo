package rpcclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
)

// TestCRLFFileRoundTripsThroughTheServer is the regression for a CRLF file
// rendering as scattered fragments. Opening it must give the editor "\n"-only
// content — a "\r" that reaches the terminal returns the cursor to column 0 —
// and saving an edit must write "\r\n" back, so the file is saved the way it
// was found and only the edited line differs.
func TestCRLFFileRoundTripsThroughTheServer(t *testing.T) {
	sock := startTestServer(t)
	r, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})

	path := filepath.Join(t.TempDir(), "migration.txt") // .txt: no language server
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\nthree\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	bufID, content, version, _, generation, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if strings.Contains(content, "\r") {
		t.Fatalf("OpenFile handed the editor a \\r: %q", content)
	}
	if content != "one\ntwo\nthree\n" {
		t.Fatalf("content = %q", content)
	}

	op := document.Op{Type: document.OpInsert, InsertLine: 1, InsertCol: 3, InsertText: "!"}
	if _, err := r.ApplyOp(ctx, bufID, op, generation, version); err != nil {
		t.Fatalf("ApplyOp: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "one\r\ntwo!\r\nthree\r\n"; string(got) != want {
		t.Errorf("saved %q, want %q — line endings were not preserved", got, want)
	}
}

// A file with mixed endings is left exactly as it is: normalizing it would make
// the next save rewrite every line.
func TestMixedLineEndingsAreSavedUnchanged(t *testing.T) {
	sock := startTestServer(t)
	r, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Disconnect(ctx) //nolint:errcheck
	})

	path := filepath.Join(t.TempDir(), "mixed.txt")
	orig := "a\r\nb\nc\r\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bufID, _, _, _, _, err := r.OpenFile(ctx, path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if err := r.Save(ctx, bufID); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Errorf("saved %q, want the mixed original %q unchanged", got, orig)
	}
}
