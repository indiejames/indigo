package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestCodeActionsOnlySendsOverlappingDiagnostics is a regression test:
// CodeActions used to send the file's entire diagnostic list as
// CodeActionContext.Diagnostics regardless of the requested range, which
// violates the LSP spec's contract for that field ("diagnostics ...
// overlapping the range provided") and let a server return a quick-fix for
// a diagnostic anywhere in the file no matter where the request was
// actually made (e.g. gopls always suggesting a far-away "replace if/else
// with max" refactor). Only diagnostics whose range overlaps the request
// should be sent.
func TestCodeActionsOnlySendsOverlappingDiagnostics(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/codeAction" {
			return nil, nil
		}
		gotParams <- params
		return nil, nil
	})
	_ = fakeServer

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	uri := pathToURI("/workspace/main.go")
	c.diagnostics = map[string][]Diagnostic{
		uri: {
			{Range: Range{Start: Position{Line: 5, Character: 0}, End: Position{Line: 5, Character: 3}}, Message: "near the request"},
			{Range: Range{Start: Position{Line: 900, Character: 0}, End: Position{Line: 900, Character: 10}}, Message: "far from the request"},
		},
	}

	done := make(chan struct{})
	go func() {
		c.CodeActions("/workspace/main.go", 5, 1, 5, 1) //nolint:errcheck
		close(done)
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the codeAction request")
	}
	var params CodeActionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if len(params.Context.Diagnostics) != 1 || params.Context.Diagnostics[0].Message != "near the request" {
		t.Errorf("context.diagnostics = %+v, want only the overlapping one (\"near the request\")", params.Context.Diagnostics)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CodeActions to return")
	}
}
