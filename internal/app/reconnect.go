package app

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/staleprompt"
)

// Reconnecting after a dropped connection (a dev container window; see
// rpcclient.Redialer). The window keeps its buffers on screen, takes no input
// until every one of them is open on the server again, and then carries on.

// handleReconnecting starts the outage: every buffer stops sending, and keys
// are refused (see blockedWhileReconnecting) until the reconnect finishes.
func (a App) handleReconnecting() App {
	a.reconnecting = true
	for i := range a.buffers {
		a.buffers[i] = a.buffers[i].Detach()
	}
	a.status = "Lost the connection to the server; reconnecting… (ctrl+c quits)"
	return a
}

// handleReconnected reopens every buffer on the new connection.
func (a App) handleReconnected(msg client.ServerReconnectedMsg) (App, tea.Cmd) {
	a.reconnectSeq = msg.Seq
	a.reattachPending = len(a.buffers)
	a.reattachFailed = 0
	if msg.Stale {
		// Same prompt as at startup: a reconnect that started a server finds
		// whatever build is installed now, which may be older than this window.
		a.staleServer = &staleprompt.Prompt{}
	}
	if len(a.buffers) == 0 {
		return a.finishReconnect()
	}
	a.status = fmt.Sprintf("Reconnected; reopening %d file(s)…", len(a.buffers))
	cmds := make([]tea.Cmd, 0, len(a.buffers))
	for _, b := range a.buffers {
		cmds = append(cmds, b.ReattachCmd(msg.Seq))
	}
	return a, tea.Batch(cmds...)
}

// handleBufferReattached counts buffers back in, and finishes the reconnect
// with the last.
func (a App) handleBufferReattached(msg client.ReattachedMsg) (App, tea.Cmd) {
	if !a.reconnecting || msg.Seq != a.reconnectSeq {
		return a, nil // from a reconnect that has since been superseded
	}
	if msg.Err != nil {
		a.reattachFailed++
	}
	a.reattachPending--
	if a.reattachPending > 0 {
		return a, nil
	}
	return a.finishReconnect()
}

// finishReconnect gives the window back to the user and tells the server what
// a new server would not know: the window's size and position in each buffer,
// and the configured directories to leave out of the file list.
func (a App) finishReconnect() (App, tea.Cmd) {
	a.reconnecting = false
	if a.reattachFailed > 0 {
		a.status = fmt.Sprintf("E: reconnected, but %d file(s) could not be reopened", a.reattachFailed)
	} else {
		a.status = "Reconnected to the server"
	}
	cmds := []tea.Cmd{a.resizeAllBuffers(), a.pushIgnoredDirs()}
	if a.active >= 0 && a.active < len(a.buffers) {
		cmds = append(cmds, a.buffers[a.active].ReportActiveContextCmd())
	}
	return a, tea.Batch(cmds...)
}

// blockedWhileReconnecting swallows input during a reconnect, and reports
// whether it did. Nothing typed could reach a server, and an edit made now
// would have to be reconciled with one the window has not reopened its
// buffers on yet. ctrl+c gives up and quits.
func (a App) blockedWhileReconnecting(msg tea.Msg) (App, tea.Cmd, bool) {
	if !a.reconnecting {
		return a, nil, false
	}
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			a.serverGone = true
			return a, tea.Quit, true
		}
		return a, nil, true
	case tea.PasteMsg, tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseMotionMsg:
		return a, nil, true
	}
	return a, nil, false
}
