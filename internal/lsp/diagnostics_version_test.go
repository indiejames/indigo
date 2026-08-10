package lsp

import (
	"encoding/json"
	"testing"
)

func newTestClientForDiagnostics(docVersions map[string]int) *Client {
	return &Client{
		docVersions: docVersions,
		diagnostics: make(map[string][]Diagnostic),
	}
}

func diagNotifyParams(t *testing.T, uri string, version int, message string) json.RawMessage {
	t.Helper()
	p := PublishDiagnosticsParams{
		URI:         uri,
		Version:     version,
		Diagnostics: []Diagnostic{{Message: message}},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal PublishDiagnosticsParams: %v", err)
	}
	return raw
}

// TestHandleNotificationDiscardsStaleDiagnostics is a regression test:
// PublishDiagnosticsParams previously had no version field at all, so a
// notification computed against an older buffer version (e.g. one still in
// flight when further edits landed) would silently overwrite newer, still-
// current diagnostics once it finally arrived.
func TestHandleNotificationDiscardsStaleDiagnostics(t *testing.T) {
	const uri = "file:///a.go"
	c := newTestClientForDiagnostics(map[string]int{uri: 3})
	c.diagnostics[uri] = []Diagnostic{{Message: "current (v3)"}}

	// A notification the server computed against version 2 arrives late,
	// after we've already moved on to version 3 via DidChange.
	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, 2, "stale (v2)"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "current (v3)" {
		t.Fatalf("diagnostics = %+v, want the v3 diagnostics to survive the stale v2 notification", got)
	}
}

// TestHandleNotificationAcceptsCurrentVersionDiagnostics verifies the
// non-stale path still works: a notification at (or past) our currently
// tracked version replaces the stored diagnostics as before.
func TestHandleNotificationAcceptsCurrentVersionDiagnostics(t *testing.T) {
	const uri = "file:///a.go"
	c := newTestClientForDiagnostics(map[string]int{uri: 2})
	c.diagnostics[uri] = []Diagnostic{{Message: "old"}}

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, 2, "new (v2)"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "new (v2)" {
		t.Fatalf("diagnostics = %+v, want the current-version notification to apply", got)
	}
}

// TestHandleNotificationAcceptsDiagnosticsWithNoVersion preserves backward
// compatibility with servers that omit the (spec-optional) version field:
// those notifications must still be applied unconditionally, since we have
// no way to judge their staleness.
func TestHandleNotificationAcceptsDiagnosticsWithNoVersion(t *testing.T) {
	const uri = "file:///a.go"
	c := newTestClientForDiagnostics(map[string]int{uri: 5})
	c.diagnostics[uri] = []Diagnostic{{Message: "old"}}

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, 0, "unversioned"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "unversioned" {
		t.Fatalf("diagnostics = %+v, want the unversioned notification to apply unconditionally", got)
	}
}
