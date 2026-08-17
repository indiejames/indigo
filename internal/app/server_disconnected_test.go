package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
)

// TestServerDisconnectedMsgQuits is a regression test: before
// ServerDisconnectedMsg existed, the client's monitor goroutine on the RPC
// connection's Done() channel only logged the server dying/crashing —
// nothing told the running Bubble Tea program to exit, so the client process
// sat on a dead connection indefinitely instead of quitting. Update must now
// mark the model as server-disconnected and return tea.Quit so main.go can
// detect it (via ServerDisconnected()) and report it after the program exits.
func TestServerDisconnectedMsgQuits(t *testing.T) {
	a := App{}

	updated, cmd := a.Update(client.ServerDisconnectedMsg{})
	a = updated.(App)

	if !a.ServerDisconnected() {
		t.Error("ServerDisconnected() = false, want true after ServerDisconnectedMsg")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil quit command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("cmd() = %T, want tea.QuitMsg", msg)
	}
}

func TestServerDisconnectedDefaultFalse(t *testing.T) {
	a := App{}
	if a.ServerDisconnected() {
		t.Error("ServerDisconnected() = true for a fresh App, want false")
	}
}
