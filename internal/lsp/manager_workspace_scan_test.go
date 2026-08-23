package lsp

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// fakeWorkspaceDiagClient wires a *Client over a net.Pipe whose other end
// answers workspace/diagnostic with a canned response for one path,
// mirroring the request/response fakes used throughout this package
// (e.g. inlay_hints_test.go) rather than driving a real process.
func fakeWorkspaceDiagClient(t *testing.T, supportsWorkspace bool, path, message string) *Client {
	t.Helper()
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() { clientEnd.Close(); serverEnd.Close() }) //nolint:errcheck

	newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, _ json.RawMessage) (any, error) {
		if method != "workspace/diagnostic" {
			return nil, nil
		}
		return map[string]any{
			"items": []map[string]any{
				{
					"kind": "full",
					"uri":  pathToURI(path),
					"items": []map[string]any{
						{"range": map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}}, "severity": 1, "message": message},
					},
				},
			},
		}, nil
	})

	c := &Client{docVersions: make(map[string]int)}
	if supportsWorkspace {
		c.caps.DiagnosticProvider = &DiagnosticOptions{WorkspaceDiagnostics: true}
	}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)
	return c
}

func waitForLSPWorkspaceScan(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !m.WorkspaceScanning() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workspace scan to finish")
}

func TestScanWorkspaceMergesAcrossSupportingClients(t *testing.T) {
	m := NewManager("/workspace", nil)
	m.clients["go"] = fakeWorkspaceDiagClient(t, true, "/workspace/a.go", "boom a")
	m.clients["rust"] = fakeWorkspaceDiagClient(t, true, "/workspace/b.rs", "boom b")

	m.ScanWorkspace()
	waitForLSPWorkspaceScan(t, m)

	snap := m.WorkspaceScanSnapshot()
	if len(snap) != 2 {
		t.Fatalf("len(snap) = %d, want 2: %+v", len(snap), snap)
	}
	if diags := snap["/workspace/a.go"]; len(diags) != 1 || diags[0].Message != "boom a" {
		t.Errorf("a.go diags = %+v, want one 'boom a'", diags)
	}
	if diags := snap["/workspace/b.rs"]; len(diags) != 1 || diags[0].Message != "boom b" {
		t.Errorf("b.rs diags = %+v, want one 'boom b'", diags)
	}
}

// TestScanWorkspaceSkipsClientsWithoutSupport is a regression test: a
// running client that never advertised workspaceDiagnostics must be
// silently skipped rather than have workspace/diagnostic attempted against
// it (which most such servers would answer with -32601 anyway, but
// ScanWorkspace shouldn't rely on that).
func TestScanWorkspaceSkipsClientsWithoutSupport(t *testing.T) {
	m := NewManager("/workspace", nil)
	m.clients["go"] = fakeWorkspaceDiagClient(t, true, "/workspace/a.go", "boom a")
	m.clients["python"] = fakeWorkspaceDiagClient(t, false, "/workspace/b.py", "should not appear")

	m.ScanWorkspace()
	waitForLSPWorkspaceScan(t, m)

	snap := m.WorkspaceScanSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snap = %+v, want exactly the supporting client's results", snap)
	}
	if _, ok := snap["/workspace/b.py"]; ok {
		t.Error("unsupported client's path should not appear in the snapshot")
	}
}

func TestScanWorkspaceCoalescesWhileRunning(t *testing.T) {
	m := NewManager("/workspace", nil)
	m.workspaceRunning = true

	m.ScanWorkspace()

	if !m.workspacePending {
		t.Error("ScanWorkspace while running should set workspacePending, not launch a new run")
	}
}

// TestScanWorkspaceSkipsDeadClients verifies runningClients filters out a
// client whose connection has already closed — Alive()==false — the same
// way clientForPath already avoids handing out a dead client for ordinary
// per-buffer requests.
func TestScanWorkspaceSkipsDeadClients(t *testing.T) {
	m := NewManager("/workspace", nil)
	c := fakeWorkspaceDiagClient(t, true, "/workspace/a.go", "boom a")
	c.closed.Store(true)
	m.clients["go"] = c

	m.ScanWorkspace()
	waitForLSPWorkspaceScan(t, m)

	if snap := m.WorkspaceScanSnapshot(); len(snap) != 0 {
		t.Errorf("snap = %+v, want empty — the only client is dead", snap)
	}
}

// TestShutdownCancelsInFlightWorkspaceScan is a regression test for
// Manager.Shutdown: without canceling scanCtx, a workspace/diagnostic
// request against an unresponsive server would hang for the full
// workspaceDiagnosticTimeout even after the server (indigo's own process)
// is shutting down.
func TestShutdownCancelsInFlightWorkspaceScan(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	go func() {
		br := bufio.NewReader(serverEnd)
		for {
			body, err := readFramedTestMessage(br)
			if err != nil {
				return
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(body, &msg); err != nil {
				return
			}
			if len(msg.ID) == 0 {
				continue // notification (e.g. "exit") — no response expected
			}
			if msg.Method == "shutdown" {
				writeFramedTestMessage(serverEnd, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil}) //nolint:errcheck
				continue
			}
			// workspace/diagnostic (or anything else): deliberately never
			// respond, so the request only ever resolves via ctx
			// cancellation.
		}
	}()

	c := &Client{docVersions: make(map[string]int)}
	c.caps.DiagnosticProvider = &DiagnosticOptions{WorkspaceDiagnostics: true}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	m := NewManager("/workspace", nil)
	m.clients["go"] = c

	m.ScanWorkspace()
	time.Sleep(20 * time.Millisecond) // let the scan goroutine issue its request

	done := make(chan struct{})
	go func() {
		m.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Manager.Shutdown() did not return promptly — scanCtx cancellation likely not wired up")
	}

	waitForLSPWorkspaceScan(t, m)
	if err := m.WorkspaceScanError("go"); err == nil {
		t.Error("expected an error recorded for the canceled workspace/diagnostic request")
	}
}
