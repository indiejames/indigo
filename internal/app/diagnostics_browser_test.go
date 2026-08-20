package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/indiejames/indigo/internal/client"
)

// TestDiagBrowserResultsMsgDiscardsStaleSequence is a regression test,
// mirroring TestGrepResultsMsgDiscardsStaleSequence: a slower, older
// GetWorkspaceDiagnostics request's results arriving after a newer one's
// (e.g. the browser was closed and reopened quickly) must not overwrite the
// current request's results.
func TestDiagBrowserResultsMsgDiscardsStaleSequence(t *testing.T) {
	a := App{diagBrowser: &diagBrowser{loading: true, seq: 2}}

	staleResult := client.WorkspaceDiagnosticsResult{Items: []client.ClientWorkspaceDiag{{Path: "stale.go"}}}
	updated, _ := a.Update(diagBrowserResultsMsg{seq: 1, result: staleResult})
	a = updated.(App)
	if len(a.diagBrowser.items) != 0 {
		t.Fatalf("stale seq=1 result was applied: %+v, want no items applied", a.diagBrowser.items)
	}
	if !a.diagBrowser.loading {
		t.Error("loading should still be true — the current request (seq 2) hasn't answered yet")
	}

	currentResult := client.WorkspaceDiagnosticsResult{Items: []client.ClientWorkspaceDiag{{Path: "current.go"}}}
	updated, _ = a.Update(diagBrowserResultsMsg{seq: 2, result: currentResult})
	a = updated.(App)
	if len(a.diagBrowser.items) != 1 || a.diagBrowser.items[0].Path != "current.go" {
		t.Errorf("items = %+v, want the current request's result applied", a.diagBrowser.items)
	}
	if a.diagBrowser.loading {
		t.Error("loading should be false once the current request's result has landed")
	}
}

// TestDiagBrowserResultsMsgSetsTruncated verifies the truncated flag from
// the RPC result is threaded through to the picker (drives the "[N+
// results]" title in View).
func TestDiagBrowserResultsMsgSetsTruncated(t *testing.T) {
	a := App{diagBrowser: &diagBrowser{loading: true, seq: 1}}

	result := client.WorkspaceDiagnosticsResult{Items: []client.ClientWorkspaceDiag{{Path: "a.go"}}, Truncated: true}
	updated, _ := a.Update(diagBrowserResultsMsg{seq: 1, result: result})
	a = updated.(App)
	if !a.diagBrowser.truncated {
		t.Error("truncated should be true, matching the RPC result")
	}
}

// TestHandleDiagBrowserKeyEnterEmitsPickedMsg verifies Enter on the
// selected item emits diagBrowserPickedMsg with that item's path/line/col.
func TestHandleDiagBrowserKeyEnterEmitsPickedMsg(t *testing.T) {
	a := App{diagBrowser: &diagBrowser{
		items: []client.ClientWorkspaceDiag{
			{Path: "a.go", Line: 3, Col: 5},
			{Path: "b.go", Line: 7, Col: 1},
		},
		cursor: 1,
	}}
	_, cmd := a.handleDiagBrowserKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	msg, ok := cmd().(diagBrowserPickedMsg)
	if !ok {
		t.Fatalf("expected diagBrowserPickedMsg, got %T", cmd())
	}
	if msg.absPath != "b.go" || msg.line != 7 || msg.col != 1 {
		t.Errorf("picked = %+v, want {absPath: b.go, line: 7, col: 1}", msg)
	}
}
