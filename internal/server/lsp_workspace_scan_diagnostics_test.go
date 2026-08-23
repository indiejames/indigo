package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	proto "github.com/indiejames/indigo/internal/proto"
)

// waitForLSPScan polls until mgr's async workspace scan has finished,
// mirroring waitForScan (workspace_scan_diagnostics_test.go) for
// lint.Manager and internal/lsp's own package-private waitForWorkspaceScan.
func waitForLSPScan(t *testing.T, mgr *lsp.Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !mgr.WorkspaceScanning() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("workspace scan did not finish in time")
}

// serveFakeWorkspaceDiagnosticLSP accepts one connection on ln and speaks
// just enough LSP to exercise Client.Initialize + Client.WorkspaceDiagnostic:
// it advertises diagnosticProvider.workspaceDiagnostics and answers
// workspace/diagnostic with a single issue for scannedPath. Hand-frames the
// Content-Length wire protocol directly (this package has no access to
// internal/lsp's own unexported test framing helpers).
func serveFakeWorkspaceDiagnosticLSP(t *testing.T, ln net.Listener, scannedPath string) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck

	br := bufio.NewReader(conn)
	for {
		body, err := readFramedLSPTestMessage(br)
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
			continue // notification (initialized, textDocument/didOpen, ...) — no response
		}
		switch msg.Method {
		case "initialize":
			writeFramedLSPTestMessage(conn, map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"diagnosticProvider": map[string]any{"workspaceDiagnostics": true},
					},
				},
			})
		case "workspace/diagnostic":
			writeFramedLSPTestMessage(conn, map[string]any{ //nolint:errcheck
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": map[string]any{
					"items": []map[string]any{
						{
							"kind": "full",
							"uri":  "file://" + scannedPath,
							"items": []map[string]any{
								{
									"range":    map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 0, "character": 1}},
									"severity": 1,
									"message":  "lsp scanned issue",
								},
							},
						},
					},
				},
			})
		case "shutdown":
			writeFramedLSPTestMessage(conn, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil}) //nolint:errcheck
		default:
			// Unhandled request (e.g. none expected in this test) — ignore.
		}
	}
}

func readFramedLSPTestMessage(br *bufio.Reader) ([]byte, error) {
	contentLen := 0
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLen, _ = strconv.Atoi(v)
		}
	}
	body := make([]byte, contentLen)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFramedLSPTestMessage(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

// TestGetWorkspaceDiagnosticsIncludesLSPWorkspaceScanForUnopenedFiles is an
// end-to-end regression test for Phase 5's LSP workspace/diagnostic pull
// support: a real (TCP-dialed) fake language server's workspace scan result
// for a file nobody has open must surface through
// GetWorkspaceDiagnostics/Summary, the same way an unopened file's lint
// scan result already does (see
// TestGetWorkspaceDiagnosticsIncludesScannedUnopenedFiles).
func TestGetWorkspaceDiagnosticsIncludesLSPWorkspaceScanForUnopenedFiles(t *testing.T) {
	workDir := t.TempDir()
	scannedPath := filepath.Join(workDir, "lsp_scanned.go")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	go serveFakeWorkspaceDiagnosticLSP(t, ln, scannedPath)

	lspMgr := lsp.NewManager(workDir, []lsp.ServerConfig{
		{Extensions: []string{"go"}, Address: ln.Addr().String()},
	})
	// DidOpen synchronously dials+initializes the TCP client via
	// clientForPath before returning — no separate open buffer/bufferEntry
	// is needed for this test, since ScanWorkspace only cares about
	// currently-running clients, not the server's own buffer table.
	lspMgr.DidOpen(filepath.Join(workDir, "trigger.go"), "package main\n")

	lspMgr.ScanWorkspace()
	waitForLSPScan(t, lspMgr)

	s := &editorService{
		cfg:     &config.Config{},
		buffers: map[uint32]*bufferEntry{},
		lspMgr:  lspMgr,
		lintMgr: lint.NewManager(&config.Config{}, workDir),
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnostics(context.Background(), func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnostics errored: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != 1 {
		t.Fatalf("Items().Len() = %d, want 1", items.Len())
	}
	path, _ := items.At(0).Path()
	if path != scannedPath {
		t.Errorf("Path() = %q, want %q", path, scannedPath)
	}
	msg, _ := items.At(0).Message_()
	if msg != "lsp scanned issue" {
		t.Errorf("Message() = %q, want %q", msg, "lsp scanned issue")
	}
}
