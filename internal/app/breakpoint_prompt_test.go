package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/server"
)

// promptApp is an App on a real server (no debugger needed) with main.go open
// and the cursor on its second line.
func promptApp(t *testing.T) (App, *pushQueue, *client.RPC, string) {
	t.Helper()
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte("package main\n\nfunc main() {\n\tprintln(1)\n}\n"), 0o644) //nolint:errcheck
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bufID, content, version, _, gen, err := rpc.OpenFile(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	a := *New(rpc, bufID, content, version, main, &config.Config{LineNumbers: true}, false, dir, 3, gen)
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 20})
	return updated.(App), q, rpc, main
}

func pressRun(t *testing.T, a App, keys ...string) App {
	t.Helper()
	for _, k := range keys {
		updated, cmd := a.Update(key(k))
		a = run(t, updated.(App), cmd, 5*time.Second, 0)
	}
	return a
}

func typeText(t *testing.T, a App, text string) App {
	t.Helper()
	for _, r := range text {
		updated, cmd := a.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		a = run(t, updated.(App), cmd, 5*time.Second, 0)
	}
	return a
}

// Space d B asks for a condition; Enter sets a conditional breakpoint on the
// server, and the push brings it back to the gutter with its note. Space d L
// then adds a log message without losing the condition.
func TestBreakpointConditionPromptSetsItOnTheServer(t *testing.T) {
	a, q, rpc, main := promptApp(t)
	a = pressRun(t, a, "space", "d", "B")
	if a.pluginInput == nil || !strings.Contains(a.pluginInput.title, "line 4") {
		t.Fatalf("no condition prompt for line 4: %+v", a.pluginInput)
	}
	a = typeText(t, a, "n > 1")
	a = pressRun(t, a, "enter")
	if a.pluginInput != nil {
		t.Error("the prompt stayed open after Enter")
	}
	a = pump(t, a, q, "the conditional breakpoint", func(a App) bool {
		return strings.Contains(strings.Join(activeRows(a), "\n"), "◉") &&
			strings.Contains(strings.Join(activeRows(a), "\n"), "if n > 1")
	})

	a = pressRun(t, a, "space", "d", "L")
	if a.pluginInput == nil || a.pluginInput.text != "" {
		t.Fatalf("logpoint prompt = %+v, want an empty one", a.pluginInput)
	}
	a = typeText(t, a, "n={n}")
	a = pressRun(t, a, "enter")
	a = pump(t, a, q, "the logpoint", func(a App) bool {
		return strings.Contains(strings.Join(activeRows(a), "\n"), "log: n={n}  if n > 1")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bps, _, err := rpc.ListBreakpoints(ctx, main)
	if err != nil || len(bps) != 1 || bps[0].Line != 3 || bps[0].Condition != "n > 1" || bps[0].LogMessage != "n={n}" {
		t.Errorf("server has %+v, %v", bps, err)
	}

	// Reopening the condition prompt shows the condition; Esc changes nothing.
	a = pressRun(t, a, "space", "d", "B")
	if a.pluginInput == nil || a.pluginInput.text != "n > 1" {
		t.Fatalf("condition prompt = %+v, want it pre-filled", a.pluginInput)
	}
	a = typeText(t, a, " && false")
	a = pressRun(t, a, "esc")
	if a.pluginInput != nil {
		t.Error("Esc did not close the prompt")
	}
	time.Sleep(100 * time.Millisecond)
	if bps, _, _ := rpc.ListBreakpoints(ctx, main); bps[0].Condition != "n > 1" {
		t.Errorf("Esc changed the condition to %q", bps[0].Condition)
	}
}
