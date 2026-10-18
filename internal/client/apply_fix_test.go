package client

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// runSequence runs each command of the tea.Sequence an edit returns, in order,
// folding each message back into the client: the sends first, then the tail.
func runSequence(t *testing.T, cl *otClient, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	v := reflect.ValueOf(cmd())
	if v.Kind() != reflect.Slice {
		t.Fatalf("expected a tea.Sequence, got %T", cmd())
	}
	for i := 0; i < v.Len(); i++ {
		if c, ok := v.Index(i).Interface().(tea.Cmd); ok && c != nil {
			for _, msg := range collectMsgs(c) {
				cl.integrate(t, msg)
			}
		}
	}
}

// TestApplyingAReplacementFixReachesTheWindow is a regression test. A plugin
// quick fix that replaces text (a spelling suggestion, say) was sent to the
// server with ApplyOp directly, never applied to the window's own buffer. That
// worked while the server delivered every op to every window; since operational
// transform it does not send a window its own ops back, so the window that
// applied the fix went on showing the old text while the server and every
// other window showed the new, and stayed diverged after further typing.
//
// It drives the fix through the popup's Enter key and a real server.
func TestApplyingAReplacementFixReachesTheWindow(t *testing.T) {
	dir, sock := startOTServer(t)
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("helo world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dialOTClient(t, sock, path, dir, "a")
	other := dialOTClient(t, sock, path, dir, "other")

	a.m.fixItems = []ClientFixItem{{Label: "hello", Replace: "hello", FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 4}}
	updated, cmd := a.m.handleFixPopup(tea.KeyPressMsg{Code: tea.KeyEnter})
	a.m = updated.(Model)
	if got := a.m.buf.Content(); got != "hello world\n" {
		t.Fatalf("right after Enter the window holds %q; the fix must apply here at once", got)
	}
	runSequence(t, a, cmd)
	a.settle(t)
	other.settle(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, _, _, _, err := a.rpc.GetBufferSnapshot(ctx, a.m.bufID)
	if err != nil {
		t.Fatal(err)
	}
	if server != "hello world\n" {
		t.Fatalf("server holds %q, want the fix applied", server)
	}
	if got := a.m.buf.Content(); got != server {
		t.Errorf("the window that applied the fix holds %q, the server %q", got, server)
	}
	if got := other.m.buf.Content(); got != server {
		t.Errorf("another window holds %q, the server %q", got, server)
	}

	// Typing afterwards must still converge.
	a.edit(t, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "> "})
	a.settle(t)
	other.settle(t)
	server, _, _, _, err = a.rpc.GetBufferSnapshot(ctx, a.m.bufID)
	if err != nil {
		t.Fatal(err)
	}
	if a.m.buf.Content() != server || other.m.buf.Content() != server {
		t.Errorf("after typing: window %q, other %q, server %q", a.m.buf.Content(), other.m.buf.Content(), server)
	}
}
