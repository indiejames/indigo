package app

import (
	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/staleprompt"
)

// handleStaleServerKey routes a key to the stale-server prompt
// (internal/staleprompt) while it is showing.
func (a App) handleStaleServerKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	p, outcome := a.staleServer.Key(km.String())
	a.staleServer = &p
	switch outcome {
	case staleprompt.Dismissed:
		a.staleServer = nil
	case staleprompt.QuitRequested:
		a.staleServer = nil
		// Not forced: a buffer restored from a recovery file is dirty, and
		// quitting must not throw that away. handleQuitAll refuses with a
		// status message instead.
		return a.handleQuitAll(client.QuitAllMsg{})
	}
	return a, nil // swallow every key while the prompt is showing
}

// newStalePrompt returns the prompt to show at startup, or nil when the server
// is current.
func newStalePrompt(rpc *client.RPC) *staleprompt.Prompt {
	if !rpc.ServerStale() {
		return nil
	}
	return &staleprompt.Prompt{}
}
