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

// diagNotifyParams builds a publishDiagnostics notification. version == nil
// means the field is omitted entirely (as some servers do); pass a pointer
// (e.g. via intPtr) for an explicit version, including 0.
func diagNotifyParams(t *testing.T, uri string, version *int, message string) json.RawMessage {
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

func intPtr(n int) *int { return &n }

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
	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, intPtr(2), "stale (v2)"))

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

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, intPtr(2), "new (v2)"))

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

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, nil, "unversioned"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "unversioned" {
		t.Fatalf("diagnostics = %+v, want the unversioned notification to apply unconditionally", got)
	}
}

// TestHandleNotificationDiscardsExplicitVersionZeroWhenStale is a
// regression test for the *int change itself: with a plain int, an explicit
// version 0 is indistinguishable from an omitted field (both marshal/read
// as the zero value), so a stale notification claiming version 0 would have
// skipped the staleness check entirely and been wrongly accepted.
func TestHandleNotificationDiscardsExplicitVersionZeroWhenStale(t *testing.T) {
	const uri = "file:///a.go"
	c := newTestClientForDiagnostics(map[string]int{uri: 3})
	c.diagnostics[uri] = []Diagnostic{{Message: "current (v3)"}}

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, intPtr(0), "stale explicit v0"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "current (v3)" {
		t.Fatalf("diagnostics = %+v, want the v3 diagnostics to survive the stale explicit-v0 notification", got)
	}
}

// TestHandleNotificationDiscardsFutureVersion covers the strict-equality
// choice: a notification claiming a version ahead of what we've tracked
// should never legitimately happen (the server can only echo a version we
// sent), so it's rejected rather than accepted the way a >= comparison
// would.
func TestHandleNotificationDiscardsFutureVersion(t *testing.T) {
	const uri = "file:///a.go"
	c := newTestClientForDiagnostics(map[string]int{uri: 2})
	c.diagnostics[uri] = []Diagnostic{{Message: "current (v2)"}}

	c.handleNotification("textDocument/publishDiagnostics", diagNotifyParams(t, uri, intPtr(5), "future (v5)"))

	got := c.GetDiagnostics("/a.go")
	if len(got) != 1 || got[0].Message != "current (v2)" {
		t.Fatalf("diagnostics = %+v, want the v2 diagnostics to survive the anomalous future-version notification", got)
	}
}
