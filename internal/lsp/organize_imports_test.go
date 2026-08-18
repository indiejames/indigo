package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestOrganizeImportsSendsOnlyFilterAndFullRange verifies OrganizeImports
// requests textDocument/codeAction with Only restricted to
// "source.organizeImports" (not the diagnostic-driven request CodeActions
// sends) and a range spanning the whole file.
func TestOrganizeImportsSendsOnlyFilterAndFullRange(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/codeAction" {
			return nil, nil
		}
		gotParams <- params
		return []map[string]any{
			{
				"title": "Organize Imports",
				"kind":  "source.organizeImports",
				"edit": map[string]any{
					"changes": map[string]any{
						pathToURI("/workspace/main.go"): []map[string]any{
							{
								"range":   map[string]any{"start": map[string]any{"line": 0, "character": 0}, "end": map[string]any{"line": 1, "character": 0}},
								"newText": "import \"fmt\"\n",
							},
						},
					},
				},
			},
		}, nil
	})
	_ = fakeServer

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		actions []CodeAction
		err     error
	}
	done := make(chan result, 1)
	go func() {
		actions, err := c.OrganizeImports("/workspace/main.go", 42)
		done <- result{actions, err}
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
	if len(params.Context.Only) != 1 || params.Context.Only[0] != "source.organizeImports" {
		t.Errorf("context.only = %v, want [\"source.organizeImports\"]", params.Context.Only)
	}
	if params.Range.Start.Line != 0 || params.Range.End.Line != 42 {
		t.Errorf("range = %+v, want start.line=0 end.line=42 (the whole file)", params.Range)
	}

	var r result
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OrganizeImports to return")
	}
	if r.err != nil {
		t.Fatalf("OrganizeImports returned an error: %v", r.err)
	}
	if len(r.actions) != 1 || r.actions[0].Kind != "source.organizeImports" {
		t.Fatalf("actions = %+v, want one source.organizeImports action", r.actions)
	}
}

// TestOrganizeImportsNullResponse verifies a null response (unsupported by
// this language server) returns no actions, not an error.
func TestOrganizeImportsNullResponse(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		return nil, nil
	})
	_ = fakeServer

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	done := make(chan error, 1)
	var actions []CodeAction
	go func() {
		var err error
		actions, err = c.OrganizeImports("/workspace/main.go", 10)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("OrganizeImports returned an error: %v", err)
		}
		if actions != nil {
			t.Errorf("actions = %v, want nil for a null response", actions)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OrganizeImports to return")
	}
}
