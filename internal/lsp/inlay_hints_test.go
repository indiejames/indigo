package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestInlayHintsSendsRangeAndDecodesBothLabelShapes verifies InlayHints sends
// the requested range and correctly decodes both label shapes the LSP spec
// allows: a plain string, and the structured []InlayHintLabelPart form some
// servers use (joining part values with Text()).
func TestInlayHintsSendsRangeAndDecodesBothLabelShapes(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/inlayHint" {
			return nil, nil
		}
		gotParams <- params
		return []map[string]any{
			{"position": map[string]any{"line": 2, "character": 8}, "label": ": string", "kind": 1},
			{"position": map[string]any{"line": 3, "character": 4}, "label": []map[string]any{{"value": "name"}, {"value": ":"}}, "kind": 2, "paddingRight": true},
		}, nil
	})
	_ = fakeServer

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		hints []InlayHint
		err   error
	}
	done := make(chan result, 1)
	go func() {
		hints, err := c.InlayHints("/workspace/main.go", 0, 0, 50, 0)
		done <- result{hints, err}
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the inlayHint request")
	}
	var params InlayHintParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Range.Start.Line != 0 || params.Range.End.Line != 50 {
		t.Errorf("range = %+v, want start.line=0 end.line=50 (the requested viewport)", params.Range)
	}

	var r result
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for InlayHints to return")
	}
	if r.err != nil {
		t.Fatalf("InlayHints returned an error: %v", r.err)
	}
	if len(r.hints) != 2 {
		t.Fatalf("got %d hints, want 2", len(r.hints))
	}
	if got := r.hints[0].Text(); got != ": string" {
		t.Errorf("hint[0].Text() = %q, want %q (plain string label)", got, ": string")
	}
	if got := r.hints[1].Text(); got != "name:" {
		t.Errorf("hint[1].Text() = %q, want %q (joined structured label parts)", got, "name:")
	}
	if r.hints[1].Kind != InlayHintKindParameter {
		t.Errorf("hint[1].Kind = %v, want InlayHintKindParameter", r.hints[1].Kind)
	}
	if !r.hints[1].PaddingRight {
		t.Error("hint[1].PaddingRight should be true")
	}
}

// TestInlayHintsNullResponse verifies a null response (no hints / unsupported)
// returns an empty slice, not an error.
func TestInlayHintsNullResponse(t *testing.T) {
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
	var hints []InlayHint
	go func() {
		var err error
		hints, err = c.InlayHints("/workspace/main.go", 0, 0, 10, 0)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InlayHints returned an error: %v", err)
		}
		if hints != nil {
			t.Errorf("hints = %v, want nil for a null response", hints)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for InlayHints to return")
	}
}
