package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

type lspEvent struct {
	kind          lspEventKind
	path, content string
}

// lspRecorder stands in for the language servers. hold, when set, parks the
// first notification until it is closed — a slow send, which is what used to
// let a later notification overtake it.
type lspRecorder struct {
	mu     sync.Mutex
	events []lspEvent
	hold   chan struct{}
	first  chan struct{}
}

func recordLSP(s *editorService) *lspRecorder {
	r := &lspRecorder{first: make(chan struct{}, 1)}
	s.lspSendHook = func(kind lspEventKind, path, content string) {
		select {
		case r.first <- struct{}{}:
			if r.hold != nil {
				<-r.hold
			}
		default:
		}
		r.mu.Lock()
		r.events = append(r.events, lspEvent{kind, path, content})
		r.mu.Unlock()
	}
	return r
}

// wait returns the events once n have arrived.
func (r *lspRecorder) wait(t *testing.T, n int) []lspEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		got := append([]lspEvent(nil), r.events...)
		r.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d notification(s) %+v, want %d", len(got), got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLSPNotificationsArriveInOrderWithTheLatestContent: every didChange used
// to be its own goroutine carrying the content of its moment, so two in quick
// succession could arrive in either order, and the language server kept
// whichever came last — older text, with the newer version number.
func TestLSPNotificationsArriveInOrderWithTheLatestContent(t *testing.T) {
	const path = "/w/a.go"
	s := &editorService{buffers: map[uint32]*bufferEntry{1: {buf: document.New(path, "v0\n")}}}
	r := recordLSP(s)
	r.hold = make(chan struct{})

	s.notifyLSP(lspOpen, path)
	<-r.first // the open is being sent, and is slow
	for _, text := range []string{"v1\n", "v2\n", "v3\n"} {
		s.mu.Lock()
		s.buffers[1].buf = document.New(path, text)
		s.notifyLSP(lspChange, path)
		s.mu.Unlock()
	}
	close(r.hold)
	got := r.wait(t, 2)
	if got[0].kind != lspOpen {
		t.Fatalf("first notification = %+v, want the open", got[0])
	}
	last := got[len(got)-1]
	if last.kind != lspChange || last.content != "v3\n" {
		t.Fatalf("last notification = %+v, want a change carrying the current text", last)
	}
	if len(got) != 2 {
		t.Errorf("changes queued behind one another were not collapsed: %+v", got)
	}

	s.mu.Lock()
	delete(s.buffers, 1)
	s.notifyLSP(lspClose, path)
	s.mu.Unlock()
	if got := r.wait(t, 3); got[2].kind != lspClose {
		t.Fatalf("notification after close = %+v, want the close", got[2])
	}
}

func lspTestService(t *testing.T, path, content string) (*editorService, proto.EditorService) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, content), canonPath: canonicalPath(path)},
		},
		lintMgr:    &lint.Manager{},
		pluginMgr:  &plugin.Manager{},
		dirWatches: make(map[string]int),
	}
	return s, proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
}

// Plugin edits, discarding recovered content and Save As all change what a
// language server should hold, and none of them told it.

func TestPluginEditNotifiesLSP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, _ := lspTestService(t, path, "hello\n")
	r := recordLSP(s)
	if err := s.PluginApplyEdit(1, 0, false, []plugin.TextEdit{{FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 5, NewText: "!"}}); err != nil {
		t.Fatal(err)
	}
	if got := r.wait(t, 1)[0]; got.kind != lspChange || got.content != "hello!\n" {
		t.Fatalf("notification = %+v, want a change with the edited text", got)
	}
}

func TestDiscardRecoveryNotifiesLSP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.go")
	s, client := lspTestService(t, path, "on disk\n")
	s.buffers[1].buf = document.New(path, "recovered\n")
	r := recordLSP(s)
	fut, rel := client.DiscardRecovery(context.Background(), func(p proto.EditorService_discardRecovery_Params) error {
		p.SetBufferId(1)
		return nil
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatal(err)
	}
	if got := r.wait(t, 1)[0]; got.kind != lspChange || got.content != "on disk\n" {
		t.Fatalf("notification = %+v, want a change back to the disk content", got)
	}
}

func TestSaveAsReopensUnderTheNewPathForLSP(t *testing.T) {
	dir := t.TempDir()
	oldPath, newPath := filepath.Join(dir, "old.go"), filepath.Join(dir, "new.go")
	s, client := lspTestService(t, oldPath, "package main\n")
	r := recordLSP(s)
	fut, rel := client.SaveAs(context.Background(), func(p proto.EditorService_saveAs_Params) error {
		p.SetBufferId(1)
		return p.SetPath(newPath)
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatal(err)
	}
	// Two documents, so two independent queues: either may be sent first.
	got := r.wait(t, 2)
	seen := map[lspEvent]bool{}
	for _, e := range got {
		seen[lspEvent{kind: e.kind, path: e.path}] = true
	}
	if !seen[lspEvent{kind: lspClose, path: oldPath}] || !seen[lspEvent{kind: lspOpen, path: newPath}] {
		t.Fatalf("notifications = %+v, want the old path closed and the new one opened", got)
	}
}
