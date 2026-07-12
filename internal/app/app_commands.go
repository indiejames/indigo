package app

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
)

// ---- async commands ----

type bufferOpenedMsg struct {
	model    client.Model
	line     int // 0-based target line; -1 = no jump
	col      int // -1 = use AtLine; >= 0 with matchLen > 0 = AtMatch; >= 0 with matchLen == 0 = AtPos
	matchLen int
}
type errorOpenMsg struct{ err error }

func (a App) doOpenFile(absPath string) tea.Cmd {
	return a.doOpenFileAt(absPath, -1)
}

func (a App) doOpenFileAt(absPath string, line int) tea.Cmd {
	// Check if already open — switch to it instead of opening again.
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			idx := i
			return func() tea.Msg { return switchBufferMsg{idx: idx, line: line, col: -1} }
		}
	}
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: -1}
	}
}

func (a App) doOpenFileAtMatch(absPath string, line, col, matchLen int) tea.Cmd {
	// Check if already open — switch to it instead of opening again.
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			idx := i
			return func() tea.Msg {
				return switchBufferMsg{idx: idx, line: line, col: col, matchLen: matchLen}
			}
		}
	}
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: col, matchLen: matchLen}
	}
}

// applyEditRecord adjusts existing jump entries that were shifted by the edit
// described in msg, then adds a new entry for msg.{Line,Col}.
func (a *App) applyEditRecord(msg client.EditRecordMsg) {
	if msg.FilePath == "" {
		return
	}
	if msg.LineDelta != 0 {
		atLine := msg.AtLine
		delta := msg.LineDelta
		n := 0
		for _, e := range a.jumpList {
			if e.filePath != msg.FilePath {
				a.jumpList[n] = e
				n++
				continue
			}
			if delta < 0 {
				// Lines [atLine, atLine-delta) were deleted.
				deletedTo := atLine - delta
				if e.line >= deletedTo {
					e.line += delta // shift back
				} else if e.line >= atLine && e.active {
					// Active entry inside the deleted range: suspend it rather
					// than discarding. It will be restored if the delete is undone.
					e.active = false
					e.deactivatedDepth = msg.UndoDepth
				}
				// Inactive entries in the range keep their stored position and
				// deactivatedDepth; they're already suspended by an earlier delete.
				a.jumpList[n] = e
				n++
			} else {
				// Lines were inserted; shift entries that follow the insertion point.
				if e.line > atLine {
					e.line += delta
				}
				a.jumpList[n] = e
				n++
			}
		}
		a.jumpList = a.jumpList[:n]
	}
	a.recordEdit(msg.FilePath, msg.Line, msg.Col, msg.UndoDepth)
}

// recordEdit appends {filePath, line, col, undoDepth} to the jump list.
// A new edit invalidates forward history; consecutive edits on the same
// file+line collapse into one entry (updating col and undoDepth in-place).
func (a *App) recordEdit(filePath string, line, col, undoDepth int) {
	if filePath == "" {
		return
	}
	if a.jumpIdx >= 0 {
		// Truncate any forward entries — a new edit rewrites the future.
		a.jumpList = a.jumpList[:a.jumpIdx+1]
		a.jumpIdx = -1
	}
	a.jumpList = append(a.jumpList, jumpEntry{filePath: filePath, line: line, col: col, undoDepth: undoDepth, active: true})
	if len(a.jumpList) > 100 {
		a.jumpList = a.jumpList[len(a.jumpList)-100:]
	}
}

// handleUndoJump processes a single undo in a single pass over the jump list:
//
//  1. Active entries created by the undone edit (undoDepth > newDepth) are
//     dropped — those edits no longer exist.
//
//  2. Inactive entries that were suspended by the undone edit
//     (deactivatedDepth > newDepth) are reactivated. Their stored line number
//     is already correct (it was frozen at the time they were suspended), so no
//     line adjustment is applied to them.
//
//  3. All other entries (both active and inactive) have their line numbers
//     adjusted to account for the buffer change the undo caused. Inactive
//     entries need this too so their positions stay valid when reactivated by a
//     future undo.
func (a *App) handleUndoJump(msg client.UndoMsg) {
	atLine := msg.AtLine
	delta := msg.LineDelta
	deletedTo := atLine - delta // only meaningful when delta < 0

	n := 0
	for _, e := range a.jumpList {
		if e.filePath == msg.FilePath {
			// Rule 1: drop active entries whose creating edit is now gone.
			if e.active && e.undoDepth > msg.NewDepth {
				continue
			}
			// Rule 2: reactivate inactive entries suspended by the undone edit.
			// Skip line adjustment — the stored position is already correct.
			if !e.active && e.deactivatedDepth > msg.NewDepth {
				e.active = true
				e.deactivatedDepth = 0
				a.jumpList[n] = e
				n++
				continue
			}
			// Rule 3: adjust line numbers for everything else.
			if delta < 0 {
				// Undo of a forward insert: the inserted lines are being removed.
				if e.line >= deletedTo {
					e.line += delta
				}
				// Entries in [atLine, deletedTo) are inserted-content entries;
				// they were already dropped by Rule 1 (undoDepth > newDepth).
				// Inactive entries from prior deletes that happen to sit in this
				// range keep their position — their deactivatedDepth is older and
				// will be handled by a future undo.
			} else {
				// Undo of a forward delete: the deleted lines are being restored.
				// Use >= so entries that landed exactly at atLine (shifted from
				// deletedTo by the original delete) are correctly restored.
				if e.line >= atLine {
					e.line += delta
				}
			}
		}
		a.jumpList[n] = e
		n++
	}
	a.jumpList = a.jumpList[:n]
	// Reset navigation state. After an undo the cursor is at the undo-restored
	// position, not at any known jump entry. Leaving jumpIdx set (possibly
	// clamped by earlier pruning) causes doJumpBack to think we're already at
	// the oldest entry and do nothing. Starting fresh from -1 lets the next
	// jump-back find the most recent surviving active entry.
	a.jumpIdx = -1
}

func (a App) doJumpBack() (tea.Model, tea.Cmd) {
	// Search backward from one before the current position (or from the end
	// if not navigating) for the nearest active entry.
	start := len(a.jumpList) - 1
	if a.jumpIdx >= 0 {
		start = a.jumpIdx - 1
	}
	for i := start; i >= 0; i-- {
		if a.jumpList[i].active {
			a.jumpIdx = i
			return a.jumpToEntry(a.jumpList[i])
		}
	}
	return a, nil
}

func (a App) doJumpForward() (tea.Model, tea.Cmd) {
	if a.jumpIdx < 0 {
		return a, nil
	}
	for i := a.jumpIdx + 1; i < len(a.jumpList); i++ {
		if a.jumpList[i].active {
			a.jumpIdx = i
			return a.jumpToEntry(a.jumpList[i])
		}
	}
	return a, nil
}

// jumpToEntry switches to (or opens) the buffer for e and positions the cursor.
func (a App) jumpToEntry(e jumpEntry) (tea.Model, tea.Cmd) {
	for i, m := range a.buffers {
		if m.FilePath() == e.filePath {
			a.active = i
			a.buffers[i] = m.AtPos(e.line, e.col, a.bufHeight())
			return a, nil
		}
	}
	return a, a.doOpenFileAtPos(e.filePath, e.line, e.col)
}

// doOpenFileAtPos opens absPath in a new buffer positioned at (line, col).
func (a App) doOpenFileAtPos(absPath string, line, col int) tea.Cmd {
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: col}
	}
}

// doReloadBuffer closes the server-side buffer and reopens it so the server
// re-reads the file from disk. The result replaces the buffer at idx in-place.
func (a App) doReloadBuffer(idx int) tea.Cmd {
	if idx < 0 || idx >= len(a.buffers) {
		appLog("doReloadBuffer: idx %d out of range (len=%d)", idx, len(a.buffers))
		return nil
	}
	m := a.buffers[idx]
	path := m.FilePath()
	oldBufID := m.BufID()
	rpc := a.rpc
	cfg := a.cfg
	appLog("doReloadBuffer: queuing cmd for idx=%d path=%q oldBufID=%d", idx, path, oldBufID)
	return func() tea.Msg {
		appLog("doReloadBuffer: cmd running, calling CloseBuffer bufID=%d", oldBufID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rpc.CloseBuffer(ctx, oldBufID); err != nil {
			appLog("doReloadBuffer: CloseBuffer error: %v", err)
		}
		appLog("doReloadBuffer: CloseBuffer done, calling OpenFile path=%q", path)
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, path)
		if err != nil {
			appLog("doReloadBuffer: OpenFile error: %v", err)
			return errorOpenMsg{err}
		}
		appLog("doReloadBuffer: OpenFile done, new bufID=%d contentLen=%d", bufID, len(content))
		newModel := client.New(rpc, bufID, content, version, path, a.workDir, cfg, fromRecovery)
		return bufferReloadedMsg{idx: idx, model: newModel}
	}
}

func (a App) doCloseAllAndQuit() tea.Cmd {
	buffers := a.buffers
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, m := range buffers {
			rpc.CloseBuffer(ctx, m.BufID()) //nolint:errcheck
		}
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}

func (a App) doSaveAllAndQuit() tea.Cmd {
	buffers := a.buffers
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, m := range buffers {
			if m.Dirty() {
				rpc.Save(ctx, m.BufID()) //nolint:errcheck
			}
			rpc.CloseBuffer(ctx, m.BufID()) //nolint:errcheck
		}
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}

func (a App) doDisconnectAndQuit() tea.Cmd {
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}
