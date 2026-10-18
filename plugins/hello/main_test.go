package main

import (
	"context"
	"slices"
	"testing"

	"github.com/indiejames/indigo/internal/plugin/plugintest"
	"github.com/indiejames/indigo/sdk"
)

// TestHelloSaysHelloOnH runs the built plugin under a real plugin manager: it
// must register H, announce itself, and answer H in normal mode.
func TestHelloSaysHelloOnH(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)

	if !slices.Contains(bridge.Keys(), "H") {
		t.Fatalf("registered keys = %q, want H", bridge.Keys())
	}
	if msg := bridge.WaitForMessage(t, "hello ready: press H to say hello"); msg.ClientID != 0 {
		t.Errorf("ready message went to client %d, want every client", msg.ClientID)
	}

	handled, edits, _, _, hasCursor, capture, err := m.HandleKey(context.Background(), "H", "normal", 1, 7, 0, 0)
	if err != nil {
		t.Fatalf("HandleKey: %v", err)
	}
	if !handled || len(edits) != 0 || hasCursor || capture != 0 {
		t.Errorf("H = handled %v, edits %v, cursor %v, capture %d; want handled and nothing else", handled, edits, hasCursor, capture)
	}
	bridge.WaitForMessage(t, "hello: activated")
}

// TestHelloIgnoresHOutsideNormalMode covers onKey's other branch: the handler
// is registered for H, but only normal mode is its to handle.
func TestHelloIgnoresHOutsideNormalMode(t *testing.T) {
	h := &Hello{}
	if resp := h.onKey("H", sdk.KeyContext{Mode: "insert"}); resp.Handled {
		t.Errorf("H in insert mode was handled: %+v", resp)
	}
	if h.active {
		t.Error("H in insert mode activated the plugin")
	}
}
