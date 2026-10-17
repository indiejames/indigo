package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// selfWriteService is a service holding one clean buffer for path, with one
// client whose FileChanged notifications arrive on the returned channel.
func selfWriteService(t *testing.T, path, content string) (*editorService, proto.EditorService, <-chan struct{}) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	notified := make(chan struct{}, 8)
	cb := proto.ClientCallback_ServerToClient(&fileChangedCallback{
		onFileChanged: func() { notified <- struct{}{} },
	})
	t.Cleanup(cb.Release)
	s, _, cl := newOTTestService(t, content, 1)
	s.cfg = &config.Config{}
	s.buffers[1].buf = document.New(path, content)
	s.buffers[1].canonPath = canonicalPath(path)
	s.clientMap = map[uint64]*clientEntry{1: {callback: cb}}
	return s, cl, notified
}

func saveBuffer(t *testing.T, cl proto.EditorService) {
	t.Helper()
	fut, rel := cl.Save(context.Background(), func(p proto.EditorService_save_Params) error {
		p.SetBufferId(1)
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func notifiedWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// TestExternalWriteRightAfterASaveIsNoticed: every event for a path was
// ignored for up to two seconds after indigo saved it, so another program
// writing the file in that window went unnoticed — and the next save wrote
// over its change. An event is now ignored only when the file holds what
// indigo wrote.
func TestExternalWriteRightAfterASaveIsNoticed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, cl, notified := selfWriteService(t, path, "package a\n")
	saveBuffer(t, cl)

	// The echo of indigo's own save.
	s.handleExternalWrite(path)
	if notifiedWithin(notified, 100*time.Millisecond) {
		t.Fatal("indigo's own save was reported as an external change")
	}

	if err := os.WriteFile(path, []byte("package a // edited elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.handleExternalWrite(path)
	if !notifiedWithin(notified, 2*time.Second) {
		t.Fatal("a write by another program straight after a save was ignored")
	}
}

// TestEchoOfAnEarlierSaveIsStillIgnored: events are handled after the fact,
// so the one for a save can be read after the next save has been recorded and
// while the file still holds the first. That is still indigo's own write.
func TestEchoOfAnEarlierSaveIsStillIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, cl, notified := selfWriteService(t, path, "package a\n")
	saveBuffer(t, cl)
	s.noteSelfWrite(path, []byte("package a // next save, not yet renamed in\n"))
	s.handleExternalWrite(path)
	if notifiedWithin(notified, 100*time.Millisecond) {
		t.Fatal("the echo of an earlier save was reported as an external change")
	}
}

// TestRestoringContentAfterAReloadIsNoticed: once indigo has adopted what is
// on disk, the file going back to something indigo once wrote is someone
// else's doing, and the buffer would otherwise keep the content it reloaded.
func TestRestoringContentAfterAReloadIsNoticed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, cl, notified := selfWriteService(t, path, "package a\n")
	saveBuffer(t, cl)
	if err := os.WriteFile(path, []byte("package a // elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fut, rel := cl.ReloadBuffer(context.Background(), func(p proto.EditorService_reloadBuffer_Params) error {
		p.SetBufferId(1)
		return nil
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil { // put back
		t.Fatal(err)
	}
	s.handleExternalWrite(path)
	if !notifiedWithin(notified, 2*time.Second) {
		t.Fatal("restoring the file after a reload was ignored")
	}
}
