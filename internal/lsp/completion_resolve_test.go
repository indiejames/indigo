package lsp

import (
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"
)

// TestResolveCompletionFetchesAdditionalTextEdits verifies that
// ResolveCompletion issues a completionItem/resolve round trip and surfaces the
// AdditionalTextEdits the server fills in — the auto-import line for a symbol
// imported from another module. The top-level textDocument/completion response
// omits these edits; without resolving the accepted item, accepting an
// auto-import completion would insert the identifier but not its import.
func TestResolveCompletionFetchesAdditionalTextEdits(t *testing.T) {
	clientEnd, serverEnd := net.Pipe()
	defer clientEnd.Close() //nolint:errcheck
	defer serverEnd.Close() //nolint:errcheck

	const importLine = "import { Foo } from \"./foo\";\n"

	fakeServer := newJSONRPCConn(serverEnd, serverEnd, nil, func(method string, params json.RawMessage) (any, error) {
		if method != "completionItem/resolve" {
			return nil, fmt.Errorf("unexpected method %q", method)
		}
		// Echo the item back with additionalTextEdits populated, as
		// typescript-language-server does for an auto-imported symbol.
		var item CompletionItem
		if err := json.Unmarshal(params, &item); err != nil {
			return nil, err
		}
		item.AdditionalTextEdits = []TextEdit{{
			Range:   Range{Start: Position{Line: 0, Character: 0}, End: Position{Line: 0, Character: 0}},
			NewText: importLine,
		}}
		return item, nil
	})
	_ = fakeServer // background readLoop stops when serverEnd is closed above

	c := &Client{docVersions: make(map[string]int), rootURI: pathToURI("/workspace")}
	c.conn = newJSONRPCConn(clientEnd, clientEnd, nil, nil)

	type result struct {
		item CompletionItem
		err  error
	}
	done := make(chan result, 1)
	go func() {
		item, err := c.ResolveCompletion(CompletionItem{Label: "Foo", Data: json.RawMessage(`{"entryName":"Foo"}`)})
		done <- result{item, err}
	}()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("ResolveCompletion returned an error: %v", r.err)
		}
		if len(r.item.AdditionalTextEdits) != 1 {
			t.Fatalf("expected 1 additionalTextEdit (the auto-import), got %d", len(r.item.AdditionalTextEdits))
		}
		if got := r.item.AdditionalTextEdits[0].NewText; got != importLine {
			t.Fatalf("auto-import edit = %q, want %q", got, importLine)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ResolveCompletion")
	}
}

// TestInitializeDeclaresCompletionResolveSupport is a regression test ensuring
// the initialize handshake advertises completionItem resolveSupport including
// additionalTextEdits. Without this declaration, servers are free to never
// defer (and thus never deliver) the auto-import edits that ResolveCompletion
// depends on. Mirrors TestInitializeDeclaresPublishDiagnosticsCapability.
func TestInitializeDeclaresCompletionResolveSupport(t *testing.T) {
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
	_ = fakeServer

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
	if td == nil || td.Completion == nil || td.Completion.CompletionItem == nil ||
		td.Completion.CompletionItem.ResolveSupport == nil {
		t.Fatal("initialize request did not declare completionItem resolveSupport — " +
			"servers may never deliver auto-import edits")
	}
	found := false
	for _, p := range td.Completion.CompletionItem.ResolveSupport.Properties {
		if p == "additionalTextEdits" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("resolveSupport.properties %v missing additionalTextEdits — auto-imports won't resolve",
			td.Completion.CompletionItem.ResolveSupport.Properties)
	}
}
