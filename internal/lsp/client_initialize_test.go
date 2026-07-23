package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestInitializeDeclaresPublishDiagnosticsCapability is a regression test for
// a bug where typescript-language-server silently never sent a single
// textDocument/publishDiagnostics notification (no error, no diagnostics,
// ever) because indigo's initialize request didn't declare the
// publishDiagnostics client capability. gopls doesn't require this — it was
// only caught by testing against a real typescript-language-server. See
// PublishDiagnosticsClientCapabilities's doc comment.
func TestInitializeDeclaresPublishDiagnosticsCapability(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method == "initialize" {
			gotParams <- params
		}
		return InitializeResult{}, nil
	})
	_ = fakeServer // background readLoop stops when serverEnd is closed above

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	done := make(chan error, 1)
	go func() { done <- c.Initialize() }()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the initialize request")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Initialize() returned an error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Initialize() to return")
	}

	var params InitializeParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}

	td := params.Capabilities.TextDocument
	if td == nil {
		t.Fatal("initialize request has no TextDocument capabilities at all")
	}
	if td.PublishDiagnostics == nil {
		t.Fatal("initialize request did not declare the publishDiagnostics client capability — " +
			"typescript-language-server will never send a single diagnostic without it")
	}
}
