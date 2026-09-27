package debugwin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/debug"
	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/server"
)

const e2eProgram = `package main

import "fmt"

func main() {
	x := 42
	fmt.Println("result", x) // line 7 (0-based 6)
}
`

// TestDebugWindowAgainstRealDelve opens the window on a session stopped at a
// breakpoint in a real program under real Delve, checks it shows the real stack
// and locals, and continues the program to exit from the window's own keys.
func TestDebugWindowAgainstRealDelve(t *testing.T) {
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
	os.WriteFile(main, []byte(e2eProgram), 0o644)                                      //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck

	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server: %v", err)
	}
	t.Cleanup(srv.Wait)
	rpc, err := rpcclient.Dial(server.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
	})
	var mu sync.Mutex
	var pushes []tea.Msg
	rpc.SetPushSender(func(m tea.Msg) { mu.Lock(); pushes = append(pushes, m); mu.Unlock() })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := rpc.ToggleBreakpoint(ctx, main, 6); err != nil {
		t.Fatal(err)
	}
	if err := rpc.DebugStart(ctx, rpcclient.DebugConfig{Adapter: "go", Program: dir, Cwd: dir}); err != nil {
		t.Fatalf("DebugStart: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		st, _ := rpc.DebugState(ctx)
		if st.Status == rpcclient.DebugStopped {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never stopped: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The window opens mid-session, as it usually will.
	m := New(rpc)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = drive(updated.(Model), updated.(Model).Init())
	out := screen(m)
	for _, want := range []string{"breakpoint", "main.go:7", "main.main", "x = 42"} {
		if !strings.Contains(out, want) {
			t.Errorf("window lacks %q:\n%s", want, out)
		}
	}

	// c continues; the program ends and the window says so.
	m = key(m, "c")
	deadline = time.Now().Add(30 * time.Second)
	for !strings.Contains(screen(m), "ended") {
		if time.Now().After(deadline) {
			t.Fatalf("the window never showed the end:\n%s", screen(m))
		}
		mu.Lock()
		pending := pushes
		pushes = nil
		mu.Unlock()
		for _, p := range pending {
			updated, cmd := m.Update(p)
			m = drive(updated.(Model), cmd)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Delve reports the exit status after it says terminated; let it land.
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(screen(m), "exit 0") && time.Now().Before(deadline) {
		mu.Lock()
		pending := pushes
		pushes = nil
		mu.Unlock()
		for _, p := range pending {
			updated, cmd := m.Update(p)
			m = drive(updated.(Model), cmd)
		}
		time.Sleep(20 * time.Millisecond)
	}
	final := screen(m)
	if !strings.Contains(final, "exit 0") {
		t.Errorf("window does not show the exit code:\n%s", final)
	}
	if !strings.Contains(final, "result 42") {
		t.Errorf("program output did not reach the window:\n%s", final)
	}
}
