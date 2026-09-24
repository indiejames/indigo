package agenttools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/server"
)

func TestFormatActiveContext(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ac := rpcclient.ActiveContext{Found: true, BufID: 3, FilePath: "/w/a.js", Line: 29, Col: 4, UpdatedAt: now.Add(-3 * time.Second)}

	t.Run("file and cursor, 1-based", func(t *testing.T) {
		out := formatActiveContext(ac, rpcclient.ActiveSelection{}, 1, nil, now)
		for _, want := range []string{"Active file: /w/a.js", "line 30, column 5", "Selection: none", "3s ago"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	})
	t.Run("character selection", func(t *testing.T) {
		sel := rpcclient.ActiveSelection{Found: true, BufID: 3, StartLine: 9, StartCol: 0, EndLine: 11, EndCol: 7}
		out := formatActiveContext(ac, sel, 1, nil, now)
		if !strings.Contains(out, "line 10 column 1 to line 12 column 8") {
			t.Errorf("selection not reported 1-based:\n%s", out)
		}
	})
	t.Run("line selection", func(t *testing.T) {
		sel := rpcclient.ActiveSelection{Found: true, BufID: 3, StartLine: 9, EndLine: 11, IsLine: true}
		if out := formatActiveContext(ac, sel, 1, nil, now); !strings.Contains(out, "lines 10-12 (whole lines)") {
			t.Errorf("line selection not reported:\n%s", out)
		}
	})
	t.Run("a selection in another buffer is not this file's", func(t *testing.T) {
		sel := rpcclient.ActiveSelection{Found: true, BufID: 99, StartLine: 1, EndLine: 2}
		if out := formatActiveContext(ac, sel, 1, nil, now); !strings.Contains(out, "Selection: none") {
			t.Errorf("reported another buffer's selection:\n%s", out)
		}
	})
	t.Run("closed since", func(t *testing.T) {
		if out := formatActiveContext(ac, rpcclient.ActiveSelection{}, 0, nil, now); !strings.Contains(out, "since been closed") {
			t.Errorf("a closed buffer was reported as open:\n%s", out)
		}
	})
	t.Run("could not check", func(t *testing.T) {
		out := formatActiveContext(ac, rpcclient.ActiveSelection{}, 0, errors.New("boom"), now)
		if !strings.Contains(out, "Could not confirm") || strings.Contains(out, "since been closed") {
			t.Errorf("an unverified buffer was reported as closed:\n%s", out)
		}
	})
	t.Run("untitled", func(t *testing.T) {
		u := ac
		u.FilePath = ""
		if out := formatActiveContext(u, rpcclient.ActiveSelection{}, 1, nil, now); !strings.Contains(out, "untitled") {
			t.Errorf("untitled buffer not called out:\n%s", out)
		}
	})
	t.Run("no window", func(t *testing.T) {
		if out := formatActiveContext(rpcclient.ActiveContext{}, rpcclient.ActiveSelection{}, 0, nil, now); !strings.Contains(out, "No editor window") {
			t.Errorf("no-window case not explained:\n%s", out)
		}
	})
}

// TestGetActiveContextThroughARealServer is the regression for an agent asked
// to "add X to line 30 of the currently open file" answering that no tool
// reports the active buffer. A window reports its position to the server; the
// agent's own, separate connection must be able to read it back through the
// tool it is given.
func TestGetActiveContextThroughARealServer(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	t.Setenv("INDIGO_PLUGINS_DIR", t.TempDir())
	dir := t.TempDir()
	srv, err := server.New(dir)
	if err != nil {
		t.Skipf("cannot start a server here: %v", err)
	}
	t.Cleanup(srv.Wait)
	sock := server.SocketPath(dir)

	dial := func() *rpcclient.RPC {
		r, err := rpcclient.Dial(sock)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r.Disconnect(ctx) //nolint:errcheck
		})
		return r
	}
	window, agent := dial(), dial()

	path := filepath.Join(dir, "migration.txt") // .txt: no language server
	if err := os.WriteFile(path, []byte(strings.Repeat("x\n", 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	bufID, _, _, _, _, err := window.OpenFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := window.SetActiveContext(ctx, window.ClientID(), bufID, path, 29, 0); err != nil {
		t.Fatal(err)
	}

	out, isErr := ExecTool(ctx, agent, nil, dir, "get_active_context", nil)
	if isErr {
		t.Fatalf("get_active_context failed: %s", out)
	}
	if !strings.Contains(out, "Active file: "+path) || !strings.Contains(out, "line 30,") {
		t.Errorf("tool did not report the window's file and line:\n%s", out)
	}
	if strings.Contains(out, "since been closed") {
		t.Errorf("an open buffer was reported closed:\n%s", out)
	}
}

// TestEveryToolIsExposedOverMCP guards the third layer of registering a tool.
// A tool needs a definition in AllTools, a case in ExecTool — and a place in
// mcpTools' exposed set, or no agent can see it. The diagnostic tools had the
// first two for weeks without the third, and nothing noticed, because tests of
// the definition and the dispatcher both pass without it.
func TestEveryToolIsExposedOverMCP(t *testing.T) {
	// Deliberately not exposed: the agent's own Glob/Grep cover these, and
	// disk-based listing and search have no buffer-consistency problem.
	notExposed := map[string]bool{"list_files": true, "search_files": true}

	exposed := map[string]bool{}
	for _, tool := range mcpTools() {
		exposed[tool.Name] = true
	}
	for _, td := range AllTools() {
		if !notExposed[td.Name] && !exposed[td.Name] {
			t.Errorf("%s is defined but not exposed over MCP — add it to mcpTools, or to notExposed here with the reason", td.Name)
		}
	}
	if !exposed["get_active_context"] {
		t.Error("get_active_context is not exposed")
	}
	for _, tool := range mcpTools() {
		if tool.Name == "get_active_context" && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Error("get_active_context should be marked read-only so it runs without a prompt")
		}
	}
}
