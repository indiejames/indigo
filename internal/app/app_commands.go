package app

import (
	"context"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

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
		bufID, content, version, fromRecovery, generation, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery, generation)
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
		bufID, content, version, fromRecovery, generation, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery, generation)
		return bufferOpenedMsg{model: m, line: line, col: col, matchLen: matchLen}
	}
}

// applyEditRecord adjusts existing jump entries that were shifted by the edit
// described in msg, then adds a new entry for msg.{Line,Col}.
func (a *App) applyEditRecord(msg client.EditRecordMsg) {
	if msg.FilePath == "" {
		return
	}
	// Each shift at its own boundary, in order. One message is still one user
	// action, so exactly one jump destination is recorded below however many
	// shifts came with it.
	for _, s := range msg.Shifts {
		a.shiftJumpEntries(msg.FilePath, s.AtLine, s.Delta, msg.UndoDepth)
	}
	a.recordEdit(msg.FilePath, msg.Line, msg.Col, msg.UndoDepth)
}

// shiftJumpEntries moves jump entries in filePath to account for a line-count
// change at atLine, without recording a new entry. Shared by a local edit (via
// applyEditRecord, which also records one) and a remote edit (which must not).
func (a *App) shiftJumpEntries(filePath string, atLine, delta, undoDepth int) {
	if filePath == "" {
		return
	}
	if delta != 0 {
		n := 0
		for _, e := range a.jumpList {
			if e.filePath != filePath {
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
					e.deactivatedDepth = undoDepth
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

// handleRedoJump adjusts the jump list for a redo.
//
// Only the line arithmetic, and it is the *forward* kind: a redo re-applies the
// edits, so entries move exactly as they did when those edits were first made,
// and an entry inside a redone delete is re-suspended. That is
// shiftJumpEntries, not handleUndoJump's Rule 3.
//
// Nothing is dropped or reactivated here. Undo's two depth rules have no
// meaning in this direction — a redo restores edits rather than removing them,
// so no entry's creating edit disappears, and nothing a redo re-applies can
// un-suspend an entry.
//
// **Entries that undo dropped do not come back.** Rule 1 deletes them from the
// list outright, so by the time a redo arrives the information is gone. That is
// a pre-existing limitation of storing the jump list this way rather than
// something this function could fix: restoring them would mean keeping dropped
// entries around with their depth, which is a larger change to the data
// structure than this is. Worth knowing when a jump-back after undo-then-redo
// has fewer places to go than expected.
func (a *App) handleRedoJump(msg client.RedoMsg) {
	// msg.NewDepth is the depth after the redo, which is the depth the original
	// edit had — so an entry re-suspended here carries the same
	// deactivatedDepth as the first time, and a later undo reactivates it.
	for _, s := range msg.Shifts {
		a.shiftJumpEntries(msg.FilePath, s.AtLine, s.Delta, msg.NewDepth)
	}
	// Same reasoning as handleUndoJump: the cursor is now at the redone
	// position, not at any known jump entry, so navigation starts fresh.
	a.jumpIdx = -1
}

// handleUndoJump processes a single undo over the jump list, in two passes:
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
	// Rules 1 and 2 are depth-based bookkeeping about the undo as a whole —
	// which edits no longer exist, which suspended entries come back — so they
	// run once, however many ops the undo restored. Only Rule 3's line
	// arithmetic is per op, and it runs in the second pass below.
	//
	// reactivated carries what used to be a `continue`: Rule 2's entries keep
	// their stored position, which is already correct, so pass 2 must leave
	// them alone. In a single pass that was one keyword; split in two it has to
	// be remembered.
	reactivated := make(map[int]bool)
	n := 0
	for _, e := range a.jumpList {
		if e.filePath == msg.FilePath {
			// Rule 1: drop active entries whose creating edit is now gone.
			if e.active && e.undoDepth > msg.NewDepth {
				continue
			}
			// Rule 2: reactivate inactive entries suspended by the undone edit.
			if !e.active && e.deactivatedDepth > msg.NewDepth {
				e.active = true
				e.deactivatedDepth = 0
				reactivated[n] = true
			}
		}
		a.jumpList[n] = e
		n++
	}
	a.jumpList = a.jumpList[:n]

	// Rule 3: adjust line numbers, each shift at its own boundary and in the
	// order the ops were applied. Summing them first would move an entry
	// sitting between two of them by the wrong amount, and by nothing at all
	// when they cancel.
	for _, s := range msg.Shifts {
		deletedTo := s.AtLine - s.Delta // only meaningful when Delta < 0
		for i := range a.jumpList {
			e := &a.jumpList[i]
			if e.filePath != msg.FilePath {
				continue
			}
			// A reactivated entry skips exactly one shift: the one restoring
			// the lines it sits inside. Its position was frozen when that
			// delete suspended it, so that shift is already accounted for —
			// but every *other* op in the same undo group still moves it, and
			// skipping those too leaves it short by their combined delta.
			//
			// The identification is exact rather than heuristic. shiftJumpEntries
			// suspends an entry only when its line falls in the forward delete's
			// [atLine, deletedTo), and the undo of that delete is the positive
			// shift with the same AtLine and Delta == deletedTo-atLine — so the
			// restoring shift is the one whose restored range contains the
			// frozen line. Consumed once, so a later shift over the same range
			// is applied normally.
			if reactivated[i] && s.Delta > 0 && e.line >= s.AtLine && e.line < s.AtLine+s.Delta {
				delete(reactivated, i)
				continue
			}
			if s.Delta < 0 {
				// Undo of a forward insert: the inserted lines are being removed.
				if e.line >= deletedTo {
					e.line += s.Delta
				}
				// Entries in [AtLine, deletedTo) are inserted-content entries;
				// they were already dropped by Rule 1 (undoDepth > newDepth).
				// Inactive entries from prior deletes that happen to sit in this
				// range keep their position — their deactivatedDepth is older and
				// will be handled by a future undo.
			} else {
				// Undo of a forward delete: the deleted lines are being restored.
				// Use >= so entries that landed exactly at AtLine (shifted from
				// deletedTo by the original delete) are correctly restored.
				if e.line >= s.AtLine {
					e.line += s.Delta
				}
			}
		}
	}
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

// doOpenFileAtPos opens absPath positioned at (line, col), switching to its
// existing tab when it is already open — the same check doOpenFileAt and
// doOpenFileAtMatch make. Without it, picking a reference or symbol in the
// current file opened a second tab onto the same server buffer.
func (a App) doOpenFileAtPos(absPath string, line, col int) tea.Cmd {
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			idx := i
			return func() tea.Msg { return switchBufferMsg{idx: idx, line: line, col: col} }
		}
	}
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, generation, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, a.workDir, cfg, fromRecovery, generation)
		return bufferOpenedMsg{model: m, line: line, col: col}
	}
}

// doReloadBuffer asks the server to re-read the file from disk and replace the
// buffer's content. The result replaces the buffer at idx in-place.
//
// This used to be CloseBuffer + OpenFile, which was silently a no-op whenever a
// second window had the same file open: CloseBuffer only drops the calling
// client, so the entry survived with a non-empty client set and OpenFile's
// attach path handed the same in-memory content straight back, never touching
// disk. One RPC now does the reload server-side, so it works with any number of
// clients attached — and the other windows pick the new content up through the
// generation bump on their next poll, which is a behaviour improvement too:
// reloading in one window used to leave the others showing stale content.
func (a App) doReloadBuffer(idx int) tea.Cmd {
	if idx < 0 || idx >= len(a.buffers) {
		appLog("doReloadBuffer: idx %d out of range (len=%d)", idx, len(a.buffers))
		return nil
	}
	m := a.buffers[idx]
	path := m.FilePath()
	bufID := m.BufID()
	langOverride := m.LangOverride()
	rpc := a.rpc
	cfg := a.cfg
	appLog("doReloadBuffer: queuing cmd for idx=%d path=%q bufID=%d", idx, path, bufID)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		content, version, generation, err := rpc.ReloadBuffer(ctx, bufID)
		if err != nil {
			appLog("doReloadBuffer: ReloadBuffer error: %v", err)
			return errorOpenMsg{err}
		}
		appLog("doReloadBuffer: reloaded bufID=%d contentLen=%d generation=%d", bufID, len(content), generation)
		// fromRecovery is false: this content came from the file itself, and
		// ReloadBuffer deletes any recovery file precisely because the reload
		// discards whatever was in memory.
		newModel := client.New(rpc, bufID, content, version, path, a.workDir, cfg, false, generation).
			WithLangOverride(langOverride)
		return bufferReloadedMsg{idx: idx, oldBufID: bufID, model: newModel}
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

// newFileParentMsg answers "can a new file be created at this path?" — the
// question the New File prompt used to answer with a local os.Stat, before the
// workspace moved to the other side of an RPC.
type newFileParentMsg struct {
	path   string // the file the user asked for, not its parent
	exists bool
	isDir  bool
	err    error
}

// newFileDirCreatedMsg reports the outcome of creating a missing parent.
type newFileDirCreatedMsg struct {
	path string
	err  error
}

// checkNewFileParent stats the parent of path on the server.
func (a App) checkNewFileParent(path string) tea.Cmd {
	rpc := a.rpc
	parent := filepath.Dir(path)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exists, isDir, err := rpc.StatPath(ctx, parent)
		return newFileParentMsg{path: path, exists: exists, isDir: isDir, err: err}
	}
}

// createNewFileParent creates path's parent directory on the server.
func (a App) createNewFileParent(path string) tea.Cmd {
	rpc := a.rpc
	parent := filepath.Dir(path)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return newFileDirCreatedMsg{path: path, err: rpc.CreateDir(ctx, parent)}
	}
}
