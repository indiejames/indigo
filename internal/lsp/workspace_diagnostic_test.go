package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestSupportsWorkspaceDiagnostics(t *testing.T) {
	c := &Client{}
	if c.SupportsWorkspaceDiagnostics() {
		t.Fatal("nil DiagnosticProvider should report unsupported")
	}
	c.caps.DiagnosticProvider = &DiagnosticOptions{WorkspaceDiagnostics: false}
	if c.SupportsWorkspaceDiagnostics() {
		t.Fatal("DiagnosticProvider with WorkspaceDiagnostics=false should report unsupported")
	}
	c.caps.DiagnosticProvider = &DiagnosticOptions{WorkspaceDiagnostics: true}
	if !c.SupportsWorkspaceDiagnostics() {
		t.Fatal("DiagnosticProvider with WorkspaceDiagnostics=true should report supported")
	}
}

// TestWorkspaceDiagnosticParsesFullReportsByPath verifies WorkspaceDiagnostic
// sends an empty previousResultIds (indigo never caches result ids) and
// groups the response's full-report items by absolute path, skipping any
// "unchanged" item (no Items) since indigo never sends a resultId that
// could produce one.
func TestWorkspaceDiagnosticParsesFullReportsByPath(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "workspace/diagnostic" {
			return nil, nil
		}
		gotParams <- params
		return map[string]any{
			"items": []map[string]any{
				{
					"kind": "full",
					"uri":  "file:///workspace/main.go",
					"items": []map[string]any{
						{"range": map[string]any{"start": map[string]any{"line": 1, "character": 0}, "end": map[string]any{"line": 1, "character": 5}}, "severity": 1, "message": "undefined: foo"},
					},
				},
				{
					// Unchanged report: no items, must contribute nothing.
					"kind":     "unchanged",
					"uri":      "file:///workspace/other.go",
					"resultId": "abc123",
				},
			},
		}, nil
	})

	c := &Client{docVersions: make(map[string]int)}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		diags map[string][]Diagnostic
		err   error
	}
	done := make(chan result, 1)
	go func() {
		diags, err := c.WorkspaceDiagnostic(context.Background())
		done <- result{diags, err}
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the workspace/diagnostic request")
	}
	var params WorkspaceDiagnosticParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal request params: %v", err)
	}
	if len(params.PreviousResultIDs) != 0 {
		t.Fatalf("PreviousResultIDs = %v, want empty", params.PreviousResultIDs)
	}

	var res result
	select {
	case res = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WorkspaceDiagnostic to return")
	}
	if res.err != nil {
		t.Fatalf("WorkspaceDiagnostic returned an error: %v", res.err)
	}
	if len(res.diags) != 1 {
		t.Fatalf("diags = %v, want exactly one path", res.diags)
	}
	got, ok := res.diags["/workspace/main.go"]
	if !ok {
		t.Fatalf("diags missing /workspace/main.go, got %v", res.diags)
	}
	if len(got) != 1 || got[0].Message != "undefined: foo" {
		t.Fatalf("diags[/workspace/main.go] = %v, want one diagnostic with message %q", got, "undefined: foo")
	}
	if _, ok := res.diags["/workspace/other.go"]; ok {
		t.Fatalf("unchanged report should not appear in the result, got %v", res.diags)
	}
}

// TestWorkspaceDiagnosticTreatsMethodNotFoundAsNoResults mirrors
// Format's handling of a server that advertises a capability at
// initialize but returns -32601 on the actual request — treat it as "no
// results" rather than a hard error, matching WorkspaceDiagnostic's
// documented best-effort contract. Hand-frames the wire protocol directly
// rather than going through newJSONRPCConn's reqHandler path, which always
// maps a handler error to a fixed -32603 and can't inject an arbitrary
// code (see fakeFormattingServer in format_method_not_found_test.go, the
// same technique reused here via the shared
// readFramedTestMessage/writeFramedTestMessage helpers).
func TestWorkspaceDiagnosticTreatsMethodNotFoundAsNoResults(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	go func() {
		br := bufio.NewReader(serverEnd)
		body, err := readFramedTestMessage(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil || msg.Method != "workspace/diagnostic" {
			return
		}
		writeFramedTestMessage(serverEnd, map[string]any{ //nolint:errcheck
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"error": map[string]any{
				"code":    jsonrpcMethodNotFound,
				"message": "Method not found: workspace/diagnostic",
			},
		})
	}()

	c := &Client{docVersions: make(map[string]int)}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		diags map[string][]Diagnostic
		err   error
	}
	done := make(chan result, 1)
	go func() {
		diags, err := c.WorkspaceDiagnostic(context.Background())
		done <- result{diags, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("WorkspaceDiagnostic returned an error, want -32601 swallowed: %v", res.err)
		}
		if len(res.diags) != 0 {
			t.Fatalf("diags = %v, want empty", res.diags)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WorkspaceDiagnostic to return")
	}
}
