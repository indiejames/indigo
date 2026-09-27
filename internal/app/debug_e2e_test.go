package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/debug"
	"github.com/indiejames/indigo/internal/server"
)

const e2eDebuggee = `package main

import "fmt"

func main() {
	x := 42
	fmt.Println(x) // line 7 (0-based 6)
}
`

// pushQueue collects what the server pushes to this window.
type pushQueue struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (q *pushQueue) send(m tea.Msg) { q.mu.Lock(); q.msgs = append(q.msgs, m); q.mu.Unlock() }
func (q *pushQueue) take() []tea.Msg {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out
}

// run executes cmd and feeds what it produces back into the App, the way the
// Bubble Tea runtime would, following batches. Each command gets timeout;
// ticks are dropped rather than followed, since they reschedule forever.
func run(t *testing.T, a App, cmd tea.Cmd, timeout time.Duration, depth int) App {
	t.Helper()
	if cmd == nil || depth > 8 {
		return a
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(timeout):
		return a // a long-running command (a tick loop); not ours to wait on
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			a = run(t, a, c, timeout, depth+1)
		}
		return a
	}
	if msg == nil || strings.Contains(fmt.Sprintf("%T", msg), "tick") {
		return a
	}
	updated, next := a.Update(msg)
	return run(t, updated.(App), next, timeout, depth+1)
}

// pump delivers pending pushes until cond holds.
func pump(t *testing.T, a App, q *pushQueue, what string, cond func(App) bool) App {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond(a) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		for _, m := range q.take() {
			updated, cmd := a.Update(m)
			a = run(t, updated.(App), cmd, 3*time.Second, 0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return a
}

// TestDebuggingFromTheEditorWindow drives the editor the way a user does —
// F9 on a line, then F5 — against a real server and real Delve, and checks the
// window ends up showing the breakpoint, the stop, and the status badge. The
// unit tests cover each piece; this is the only test of the chain between
// them: key → RPC → server → push → refetch → render.
func TestDebuggingFromTheEditorWindow(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and debugs a program")
	}
	if os.Getenv("INDIGO_DEBUG_TESTS") != "1" {
		if hint := debug.PermissionHint(); hint != "" {
			t.Skip(hint + " (or set INDIGO_DEBUG_TESTS=1)")
		}
	}
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte(e2eDebuggee), 0o644)                                     //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck

	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server: %v", err)
	}
	t.Cleanup(srv.Wait)
	rpc, err := client.Dial(server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
	})
	q := &pushQueue{}
	rpc.SetPushSender(q.send)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	bufID, content, version, _, gen, err := rpc.OpenFile(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	// This window is the one the user is in: it is the one that follows a stop.
	if err := rpc.SetActiveContext(ctx, rpc.ClientID(), bufID, main, 0, 0); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LineNumbers: true}
	a := *New(rpc, bufID, content, version, main, cfg, false, dir, 6, gen) // cursor on line 7
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	a = updated.(App)

	// rowStarts finds buffer line `line` by the number in its gutter — the
	// window opens with the cursor on line 7, which scrolls, so a screen row
	// index is not a buffer line — and checks its left-gutter marker.
	rowStarts := func(a App, line int, prefix string) bool {
		for _, row := range activeRows(a) {
			r := []rune(row)
			if len(r) < 3 {
				continue
			}
			fields := strings.Fields(string(r[2:]))
			if len(fields) > 0 && fields[0] == fmt.Sprint(line+1) {
				return strings.HasPrefix(row, prefix)
			}
		}
		return false
	}

	// F9: a breakpoint on the cursor's line.
	updated, cmd := a.Update(tea.KeyPressMsg{Code: tea.KeyF9})
	a = run(t, updated.(App), cmd, 5*time.Second, 0)
	a = pump(t, a, q, "the breakpoint to appear", func(a App) bool { return rowStarts(a, 6, "●") })

	// F5: start debugging; the program stops at the breakpoint.
	updated, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	a = run(t, updated.(App), cmd, 90*time.Second, 0)
	a = pump(t, a, q, "the stop to be shown", func(a App) bool { return rowStarts(a, 6, "▶") })

	if bar := activeRows(a); !strings.Contains(strings.Join(bar, "\n"), "DEBUG breakpoint") {
		t.Errorf("status bar does not show the stop:\n%s", strings.Join(bar, "\n"))
	}

	// F5 again continues; the program runs to the end and the session ends.
	updated, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyF5})
	a = run(t, updated.(App), cmd, 15*time.Second, 0)
	a = pump(t, a, q, "the session to end", func(a App) bool {
		return a.debug != nil && a.debug.state.Status == client.DebugTerminated
	})
	if rowStarts(a, 6, "▶") {
		t.Error("the stopped-here arrow is still shown after the session ended")
	}
	if !strings.Contains(a.status, "Debug session ended") {
		t.Errorf("status = %q, want the session end reported", a.status)
	}
}
