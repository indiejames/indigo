package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/server"
)

// TestMCPModeServesToolsThroughTheDaemon drives the real binary the way Claude
// Code inside a dev container does: `indigo-server --mcp`, stdio, started in the
// workspace. It checks the two properties the mode exists for:
//
//   - a tool call works against the workspace's files, and
//   - with no server running, the one it starts is this binary's --daemon — the
//     same process an editor window's bridge starts and joins — not
//     `indigo --server`, which does not exist in a container.
func TestMCPModeServesToolsThroughTheDaemon(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "indigo-server")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0") // as shipped: no cgo
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build indigo-server without cgo: %v\n%s", err, out)
	}

	ws, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(file, []byte("hello from the container\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Isolate the daemon this test starts: its own log dir and no plugins.
	env := append(os.Environ(), "INDIGO_LOG_DIR="+t.TempDir(), "INDIGO_PLUGINS_DIR="+t.TempDir())

	mcp := exec.Command(bin, "--mcp")
	mcp.Dir = ws
	mcp.Env = env
	stdin, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := mcp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := mcp.Start(); err != nil {
		t.Fatal(err)
	}
	sock := server.SocketPath(ws)
	t.Cleanup(func() {
		stdin.Close() //nolint:errcheck
		mcp.Wait()    //nolint:errcheck
		// The daemon is detached and exits with its last client. An MCP
		// process closing is that last client here; wait for it to go so it
		// does not outlive the test.
		deadline := time.Now().Add(5 * time.Second)
		for server.IsRunning(sock) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})

	out := bufio.NewScanner(stdout)
	out.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(msg string) {
		t.Helper()
		if _, err := stdin.Write([]byte(msg + "\n")); err != nil {
			t.Fatalf("write %s: %v", msg, err)
		}
	}
	recv := func() map[string]any {
		t.Helper()
		if !out.Scan() {
			t.Fatalf("no response from --mcp: %v", out.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("bad response %q: %v", out.Text(), err)
		}
		return m
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	if r := recv(); r["error"] != nil {
		t.Fatalf("initialize: %v", r["error"])
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	args, _ := json.Marshal(map[string]any{"path": file})
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":` + string(args) + `}}`)
	r := recv()
	if r["error"] != nil {
		t.Fatalf("tools/call read_file: %v", r["error"])
	}
	raw, _ := json.Marshal(r["result"])
	if !strings.Contains(string(raw), "hello from the container") {
		t.Fatalf("read_file result does not contain the file: %s", raw)
	}

	if !server.IsRunning(sock) {
		t.Fatal("no server answering on the workspace socket after a tool call")
	}
	ps, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		t.Skipf("ps unavailable to check which server started: %v", err)
	}
	if !strings.Contains(string(ps), bin+" --daemon "+ws) {
		var indigo []string
		for _, line := range strings.Split(string(ps), "\n") {
			if strings.Contains(line, ws) && !strings.Contains(line, " ps ") {
				indigo = append(indigo, line)
			}
		}
		t.Errorf("the server --mcp started is not `%s --daemon %s`; processes for this workspace:\n%s",
			bin, ws, strings.Join(indigo, "\n"))
	}
}
