package client

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/syncevent"
)

// reattachTimeout bounds reopening one buffer on a reconnected server. The
// server may have only just started, and opening a file starts its language
// server.
const reattachTimeout = 30 * time.Second

// Detach stops this buffer talking to the server, for when the connection has
// dropped and a reconnect is under way (rpcclient.ServerReconnectingMsg).
//
// Sending is paused rather than left to fail: an edit sent now would fail,
// start a resync, and the resync would fail too. Paused, the edits stay in the
// buffer, and ReattachCmd reconciles them with the server once it is back.
// Polling stops with it, since a poll waits for the send queue to be idle.
func (m Model) Detach() Model {
	m.detached = true
	m.sendQ.pauseForResync()
	return m
}

// reconnectSeq is the number of the connection this window is on now
// (RPC.Reconnects), or 0 with no RPC, as in tests.
func (m Model) reconnectSeq() uint64 {
	if m.rpc == nil {
		return 0
	}
	return m.rpc.Reconnects()
}

// ReattachCmd reopens this buffer on the reconnected server. seq is the
// reconnect's (rpcclient.ServerReconnectedMsg.Seq), returned in ReattachedMsg.
func (m Model) ReattachCmd(seq uint64) tea.Cmd {
	bufID, path, rpc := m.bufID, m.filePath, m.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), reattachTimeout)
		defer cancel()
		res, err := rpc.Reattach(ctx, bufID, path)
		return reattachedMsg{bufID: bufID, seq: seq, res: res, err: err}
	}
}

// reattachedMsg carries the server's copy of a buffer reopened by ReattachCmd.
// Routable, because every buffer reattaches at once and only one is active.
type reattachedMsg struct {
	bufID uint32
	seq   uint64
	res   rpcclient.Reattached
	err   error
}

func (m reattachedMsg) RouteBufID() uint32 { return m.bufID }

// ReattachedMsg tells the App that one buffer has finished reattaching,
// successfully or not.
type ReattachedMsg struct {
	BufID uint32
	Seq   uint64
	Err   error
}

// handleReattached reconciles this buffer with the server's copy after a
// reconnect.
//
// The window's copy is the one the user has been looking at, and usually the
// server's is the same text. When they differ, one of them wins:
//
//   - **The window's**, normally. The server is usually a new one that read the
//     file from the recovery file its predecessor wrote on losing its last
//     client (or from disk), so what it lacks are the edits that were on their
//     way when the connection dropped. They are sent as edits, which keeps the
//     window's undo history valid.
//   - **The server's**, when another window also has the buffer open (its edits
//     made during the outage are in the server's copy), or when this buffer had
//     no unsaved changes and the server read something else from disk: the
//     file changed on disk while the window was away, and a clean buffer
//     follows its file.
func (m Model) handleReattached(msg reattachedMsg) (Model, tea.Cmd) {
	if msg.bufID != m.bufID {
		return m, nil
	}
	if msg.seq != m.reconnectSeq() {
		// The connection dropped again while this was in flight, and the
		// buffer has been detached since. Its content came from a server the
		// window is no longer connected to; the next reconnect reattaches it.
		return m, nil
	}
	bufID := m.bufID
	done := func(err error) tea.Cmd {
		return func() tea.Msg { return ReattachedMsg{BufID: bufID, Seq: msg.seq, Err: err} }
	}
	if msg.err != nil {
		clientLog("reattach FAILED buf=%d path=%s: %v", m.bufID, m.filePath, msg.err)
		syncevent.Recordf("client", syncevent.ResyncFailed, m.bufID, m.filePath, "reattach: %v", msg.err)
		m = m.pushSevereError("ERR: could not reopen this file after reconnecting: " + msg.err.Error())
		return m, done(msg.err)
	}
	res := msg.res
	hadPending := len(m.pending) > 0
	m.detached = false
	// Everything queued or in flight went to the old server. What of it the
	// new one lacks is found by comparing content below, not by resending.
	m.sendQ.discard()
	m.pending = nil
	m.nextSeq = 0
	m.version = res.Version
	m.generation = res.Generation
	m.generationKnown = true

	local := m.buf.Content()
	switch {
	case res.Content == local:
		clientLog("reattach ok buf=%d path=%s: content matches", m.bufID, m.filePath)
		return m, done(nil)

	case res.Shared || (!m.buf.Dirty() && !res.FromRecovery):
		clientLog("reattach buf=%d path=%s: adopting the server's copy (shared=%v)", m.bufID, m.filePath, res.Shared)
		m = m.adoptSnapshot(res.Content, res.Version, res.Generation, !res.Shared)
		switch {
		case res.Shared && hadPending:
			m = m.pushSevereError("Reconnected. Another window has this file open, so its copy was kept; " +
				"edits made here just before the connection dropped may be missing.")
		case res.Shared:
			m = m.pushStatus("Reconnected; took this file's content from the other window that has it open")
		default:
			m = m.pushStatus("Reconnected; this file changed on disk while disconnected and was reloaded")
		}
		return m, tea.Batch(m.reparseHighlight(), done(nil))

	default:
		ops := document.DiffOps(res.Content, local)
		clientLog("reattach buf=%d path=%s: sending %d op(s) the server lacks", m.bufID, m.filePath, len(ops))
		cmds := make([]tea.Cmd, 0, len(ops)+1)
		for _, op := range ops {
			var cmd tea.Cmd
			m, cmd = m.sendToServer(op)
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(append(cmds, done(nil))...)
	}
}

// adoptSnapshot replaces this buffer's content with the server's, discarding
// everything that described the old content: queued edits, undo history,
// selections. clean says whether the content is the file's as on disk.
func (m Model) adoptSnapshot(content string, version, generation uint64, clean bool) Model {
	m.buf = document.New(m.filePath, content)
	// Everything in flight described the old content. Keeping it would
	// rebase future incoming ops past edits that no longer exist.
	m.sendQ.discard()
	m.pending = nil
	// Reset alongside pending: that count is what retires pending entries,
	// and the server's count of ops applied from this client restarted with
	// the snapshot. The two must restart together — a server count left
	// ahead of this one retires ops it has never seen.
	m.nextSeq = 0
	m.version = version
	m.generation = generation
	m.generationKnown = true
	m.undoStack = nil
	m.redoStack = nil
	m.currentGroup = nil
	m.sel = nil
	m.extraCursors = nil
	if clean {
		m.savedUndoDepth = 0
	} else {
		// The server's content may itself be unsaved; err toward "unsaved"
		// rather than a false-clean marker. -1 never matches len(undoStack),
		// so only an explicit Save clears it.
		m.buf.MarkDirty()
		m.savedUndoDepth = -1
	}
	lc := m.buf.LineCount()
	if m.cursor.Line >= lc {
		m.cursor.Line = max(0, lc-1)
	}
	if lineLen := m.buf.LineLen(m.cursor.Line); m.cursor.Col > lineLen {
		m.cursor.Col = lineLen
	}
	m.scrollToCursor()
	return m
}
