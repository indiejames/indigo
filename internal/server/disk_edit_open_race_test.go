package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// TestOpenFileWaitsForAnEditBeingMadeOnDisk: an edit to a file nobody has open
// is made on disk, and an OpenFile landing while it was in progress read the
// file before the edit wrote it. The buffer then lacked the edit, indigo's own
// write was ignored as a self-save, and the next save wrote the edit away. The
// edit is parked here between its read and its write while the file is opened.
func TestOpenFileWaitsForAnEditBeingMadeOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, _, cl := newOTTestService(t, "", 1)
	delete(s.buffers, 1)
	if err := os.WriteFile(path, []byte("old name\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	parked, release := make(chan struct{}), make(chan struct{})
	s.diskEditHook = func() {
		close(parked)
		<-release
	}
	edited := make(chan error, 1)
	go func() {
		_, _, err := s.applyItemsToPath(1, path, []workspaceEditItem{{line: 0, col: 0, oldText: "old", newText: "new"}})
		edited <- err
	}()
	<-parked

	opened := make(chan string, 1)
	go func() {
		fut, rel := cl.OpenFile(context.Background(), func(p proto.EditorService_openFile_Params) error {
			p.SetClientId(2)
			return p.SetPath(path)
		})
		defer rel()
		res, err := fut.Struct()
		if err != nil {
			opened <- "error: " + err.Error()
			return
		}
		content, _ := res.Content()
		opened <- content
	}()
	// Give the open every chance to read the file before the edit writes it.
	select {
	case got := <-opened:
		close(release)
		<-edited
		t.Fatalf("the open completed while an edit to the file was half done, with %q", got)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	if got := <-opened; got != "new name\n" {
		t.Fatalf("opened with %q, want the edited file", got)
	}
}
