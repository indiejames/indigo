package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestOrganizeImportsNeverSendsNullDiagnostics is a regression test for a
// live bug report: OrganizeImports never sets CodeActionContext.Diagnostics,
// leaving it a nil Go slice, which encoding/json marshals as JSON `null`
// rather than `[]`. The LSP spec requires diagnostics to always be an
// array; at least one real-world TypeScript language server doesn't guard
// against `null` there and crashed with "Cannot read properties of null
// (reading 'filter')" on every Organize Imports request. codeActionRequest
// now normalizes a nil Diagnostics to an empty (non-nil) slice before
// sending — this checks the actual JSON on the wire, not just the
// unmarshaled Go value, since unmarshaling `null` vs `[]` into a Go slice
// is exactly the nil-vs-non-nil-empty distinction this bug hinges on.
func TestOrganizeImportsNeverSendsNullDiagnostics(t *testing.T) {
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

	done := make(chan struct{})
	go func() {
		c.OrganizeImports("/workspace/main.ts", 10) //nolint:errcheck
		close(done)
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the codeAction request")
	}

	assertDiagnosticsNotNullOnWire(t, raw)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for OrganizeImports to return")
	}
}

// TestCodeActionsWithNoOverlapSendsEmptyNotNullDiagnostics covers the same
// bug on the other call path through codeActionRequest: CodeActions leaves
// its locally-built Diagnostics slice nil whenever nothing overlaps the
// requested range (e.g. a clean file, or a quick-fix request far from any
// diagnostic) — this used to send the identical `"diagnostics":null` a
// picky server chokes on.
func TestCodeActionsWithNoOverlapSendsEmptyNotNullDiagnostics(t *testing.T) {
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
	// No diagnostics registered for this file at all, so CodeActions'
	// locally-built `relevant` slice stays nil.

	done := make(chan struct{})
	go func() {
		c.CodeActions("/workspace/clean.ts", 5, 1, 5, 1) //nolint:errcheck
		close(done)
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the codeAction request")
	}

	assertDiagnosticsNotNullOnWire(t, raw)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for CodeActions to return")
	}
}

// assertDiagnosticsNotNullOnWire fails t if raw's context.diagnostics field
// is JSON `null` rather than an array. Checked two ways: directly against a
// loosely-typed decode (catches `null` unambiguously) and via the strongly-
// typed CodeActionParams (nil after unmarshal implies the wire value was
// `null`, since unmarshaling `[]` always yields a non-nil empty slice).
func assertDiagnosticsNotNullOnWire(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var loose struct {
		Context struct {
			Diagnostics json.RawMessage `json:"diagnostics"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if string(loose.Context.Diagnostics) == "null" {
		t.Errorf("context.diagnostics was sent as JSON null, want []: %s", raw)
	}

	var params CodeActionParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Context.Diagnostics == nil {
		t.Error("params.Context.Diagnostics is nil after unmarshal, want a non-nil empty slice")
	}
}
