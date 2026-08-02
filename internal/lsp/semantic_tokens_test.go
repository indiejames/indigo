package lsp

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// TestSemanticTokensRangeSendsRangeAndDecodes verifies SemanticTokensRange
// sends the requested range and decodes the response using the legend
// captured from the server's capabilities.
func TestSemanticTokensRangeSendsRangeAndDecodes(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	gotParams := make(chan json.RawMessage, 1)
	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/semanticTokens/range" {
			return nil, nil
		}
		gotParams <- params
		return SemanticTokensResult{Data: []uint32{0, 0, 3, 0, 0}}, nil
	})
	_ = fakeServer

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.caps.SemanticTokensProvider = &SemanticTokensOptions{
		Legend: SemanticTokensLegend{TokenTypes: []string{"variable"}},
	}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		tokens []SemanticToken
		err    error
	}
	done := make(chan result, 1)
	go func() {
		tokens, err := c.SemanticTokensRange("/workspace/main.go", 0, 0, 50, 0)
		done <- result{tokens, err}
	}()

	var raw json.RawMessage
	select {
	case raw = <-gotParams:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the semanticTokens/range request")
	}
	var params SemanticTokensParams
	if err := json.Unmarshal(raw, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if params.Range.Start.Line != 0 || params.Range.End.Line != 50 {
		t.Errorf("range = %+v, want start.line=0 end.line=50", params.Range)
	}

	var r result
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SemanticTokensRange to return")
	}
	if r.err != nil {
		t.Fatalf("SemanticTokensRange returned an error: %v", r.err)
	}
	if len(r.tokens) != 1 || r.tokens[0].TokenType != "variable" {
		t.Errorf("tokens = %+v, want one variable token", r.tokens)
	}
}

// TestSemanticTokensRangeSkipsRequestWhenUnsupported verifies the method
// returns early, without attempting a request, when the server never
// advertised semanticTokensProvider — this will be polled periodically like
// InlayHints, so hitting an unsupporting server would otherwise waste a
// request indefinitely. conn is deliberately left nil so a broken guard
// would panic rather than silently succeed.
func TestSemanticTokensRangeSkipsRequestWhenUnsupported(t *testing.T) {
	c := &Client{docVersions: make(map[string]int)} // caps.SemanticTokensProvider is nil; conn is nil
	tokens, err := c.SemanticTokensRange("/workspace/main.go", 0, 0, 10, 0)
	if err != nil {
		t.Fatalf("SemanticTokensRange returned an error: %v", err)
	}
	if tokens != nil {
		t.Errorf("tokens = %v, want nil when the server has no semanticTokensProvider", tokens)
	}
}
