package client

import "testing"

// Regression test: plugin ShowMessage must reach the status bar. The App used
// to swallow PluginShowMsgMsg entirely, making ShowMessage a silent no-op.
func TestPluginShowMessageSetsStatus(t *testing.T) {
	m := newTestModel("hello\n")
	m2, _ := m.Update(PluginShowMsgMsg{Text: "hello: activated"})
	got := m2.(Model)
	if got.status != "hello: activated" {
		t.Errorf("status = %q, want %q", got.status, "hello: activated")
	}
}
