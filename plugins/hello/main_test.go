package main

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/plugin/plugintest"
)

// TestHelloSaysHelloFromTheCommandMenu runs the built plugin under a real
// plugin manager: it must list its item in the Command menu, announce itself,
// and say hello when the item is picked. It binds no key.
func TestHelloSaysHelloFromTheCommandMenu(t *testing.T) {
	bridge := plugintest.NewBridge()
	m, _ := plugintest.Start(t, ".", bridge)

	items := m.AllMenuItems()
	if len(items) != 1 || items[0].Label != "Hello" || items[0].Key != "h" || items[0].Command != "hello.say" {
		t.Fatalf("menu items = %+v, want one Hello item under h running hello.say", items)
	}
	if msg := bridge.WaitForMessage(t, "hello ready: press space h to say hello"); msg.ClientID != 0 {
		t.Errorf("ready message went to client %d, want every client", msg.ClientID)
	}

	handled, edits, _, _, hasCursor, capture, err := m.InvokeMenuAction(context.Background(), items[0].PluginName, items[0].Command, 1, 7, 0, 0)
	if err != nil {
		t.Fatalf("InvokeMenuAction: %v", err)
	}
	if !handled || len(edits) != 0 || hasCursor || capture != 0 {
		t.Errorf("hello.say = handled %v, edits %v, cursor %v, capture %d; want handled and nothing else", handled, edits, hasCursor, capture)
	}
	bridge.WaitForMessage(t, "hello: activated")

	if handled, _, _, _, _, _, _ := m.HandleKey(context.Background(), "H", "normal", 1, 7, 0, 0); handled {
		t.Error("H in normal mode reached the plugin; plugins bind no keys")
	}
}
