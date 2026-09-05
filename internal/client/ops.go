package client

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// ReportActiveContextCmd returns a Cmd that tells the server this client's
// current buffer, cursor position, and selection are the active context.
// Returns nil when rpc is nil (e.g. in tests).
func (m Model) ReportActiveContextCmd() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	clientID := m.rpc.ClientID()
	bufID := m.bufID
	filePath := m.filePath
	line := uint32(m.cursor.Line)
	col := uint32(m.cursor.Col)

	var sel ActiveSelection
	if m.sel != nil {
		start, end := m.sel.ordered()
		sel = ActiveSelection{
			Found:     true,
			BufID:     bufID,
			StartLine: uint32(start.Line),
			StartCol:  uint32(start.Col),
			EndLine:   uint32(end.Line),
			EndCol:    uint32(end.Col), // inclusive, matching selectedText
			IsLine:    m.sel.IsLine,
		}
	}

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.rpc.SetActiveContext(ctx, clientID, bufID, filePath, line, col)
		_ = m.rpc.SetActiveSelection(ctx, clientID, bufID, sel)
		return nil
	}
}

// sendOp applies the op locally and sends it to the server.
func (m Model) sendOp(op document.Op) tea.Cmd {
	m.buf.Apply(op)
	return m.sendToServer(op)
}

// sendToServer sends op to the server without applying it locally.
// Used by undo when local apply is handled separately.
//
// If the RPC fails, the local buffer has already applied op (or, for undo,
// whatever produced it) while the server never received it — client and
// server have now diverged. Rather than leave that silent and permanent,
// this returns applyOpFailedMsg, whose handler triggers a hard resync from
// the server's authoritative content. Unix-socket network blips are rare
// enough that a resync (visible, but never silently corrupting/losing
// content) is preferable to a retry: a retried op's line/col coordinates
// may no longer be valid if the user kept typing before the retry lands.
func (m Model) sendToServer(op document.Op) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := m.rpc.ApplyOp(ctx, m.bufID, op, m.generation)
		if err != nil {
			return applyOpFailedMsg{bufID: m.bufID, err: err}
		}
		return nil
	}
}

// resyncFromServer re-fetches this buffer's authoritative content from the
// server after an ApplyOp failure or a detected generation mismatch, to
// recover from client/server divergence. Uses GetBufferSnapshot (keyed by
// bufferID) rather than OpenFile (keyed by path): the client's own
// remembered path can itself be stale if a different client renamed this
// buffer via SaveAs since it last synced.
func (m Model) resyncFromServer() tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		content, version, generation, path, err := m.rpc.GetBufferSnapshot(ctx, bufID)
		return bufferResyncMsg{bufID: bufID, content: content, version: version, generation: generation, path: path, err: err}
	}
}

// opLineDelta returns the first affected line and the net line-count change
// produced by applying op. Insert ops that add newlines return a positive delta;
// deletes that span multiple lines return a negative delta.
func opLineDelta(op document.Op) (atLine, delta int) {
	switch op.Type {
	case document.OpInsert:
		return op.InsertLine, strings.Count(op.InsertText, "\n")
	case document.OpDelete:
		return op.FromLine, -(op.ToLine - op.FromLine)
	}
	return 0, 0
}

// minAffectedLine returns the smallest line number referenced by any op in the
// slice (which holds inverse ops from an insert session). Inverse ops reference
// the same line numbers as the corresponding forward ops.
func minAffectedLine(ops []document.Op) int {
	min := -1
	for _, op := range ops {
		l := op.InsertLine
		if op.Type == document.OpDelete {
			l = op.FromLine
		}
		if min < 0 || l < min {
			min = l
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// applyOp records the inverse of op for undo, then applies op locally and
// queues an async send to the server.
//
// If m.currentGroup is non-nil (i.e. we are inside an Insert session), the
// inverse is appended to the group instead of pushed immediately; the group is
// committed to undoStack when the user presses ESC.
// In normal mode (currentGroup == nil) an EditRecordMsg is also emitted so the
// App can adjust existing jump entries and add a new one.
func applyOp(m Model, op document.Op) (Model, tea.Cmd) {
	inv := inverseOp(m, op)
	atLine, delta := opLineDelta(op)
	var recordCmd tea.Cmd
	if m.currentGroup != nil {
		m.currentGroup = append(m.currentGroup, inv)
	} else {
		m.undoStack = append(m.undoStack, undoEntry{ops: []document.Op{inv}, before: m.cursorSnap()})
		fp, line, col := m.filePath, m.cursor.Line, m.cursor.Col
		depth := len(m.undoStack)
		recordCmd = func() tea.Msg {
			return EditRecordMsg{FilePath: fp, Line: line, Col: col, AtLine: atLine, LineDelta: delta, UndoDepth: depth}
		}
	}
	m.redoStack = nil // any new edit invalidates the redo history
	m = m.shiftLSPOverlayLines(atLine, delta)
	m, refreshCmd := m.scheduleLSPOverlayRefresh()
	// sendOp applies op to m.buf as a side effect (see its doc comment)
	// before returning the network-send cmd, so it must run before
	// scrollToCursor — scrollToCursor's wrap-chunk math needs the
	// post-edit line content to correctly detect a cursor that just
	// wrapped onto a new visual row. The caller already moved m.cursor to
	// reflect the edit before calling applyOp; re-syncing the viewport
	// here (rather than at every applyOp call site) keeps every
	// applyOp-routed edit — self-insert, backspace, tab, Enter, auto-pair,
	// ... — from ever leaving the cursor rendered off-screen.
	sendCmd := m.sendOp(op)
	m.scrollToCursor()
	return m, tea.Batch(sendCmd, m.reparseHighlight(), recordCmd, refreshCmd)
}

// shiftLSPOverlayLines re-keys cached semantic-token/inlay-hint data by delta
// for lines at or after atLine, matching how a line-count-changing edit
// (Enter, a multi-line paste/delete) shifts every subsequent buffer line.
// Leaving the cache untouched (as scheduleLSPOverlayRefresh does for a
// same-line edit) is not safe here: after such an edit, the renderer looks up
// data by the NEW line numbers, but the cached data is still sitting at the
// OLD ones — a lookup at the new number simply finds nothing, which looks
// identical to having cleared it, producing the same white-flash symptom on
// every line after the edit point. Re-keying instead of clearing keeps that
// data visible (if slightly approximate — see below) through the debounce
// window instead of losing it.
//
// Lines before atLine are unaffected by the edit and untouched. For a
// negative delta (deleted lines), a line whose shifted destination would
// still fall before atLine had its content deleted entirely and is dropped;
// this also correctly drops data for lines strictly within the deleted
// range without needing to special-case them (verified against the exact
// line arithmetic for a multi-line delete). For a positive delta (inserted
// lines, e.g. Enter splitting a line in two), the line at atLine itself is
// an approximation — its old content may have been split across the
// original and new line — but the render-time bounds check discards
// whatever columns no longer fit, and the debounced refresh corrects it
// shortly after regardless.
func (m Model) shiftLSPOverlayLines(atLine, delta int) Model {
	if delta == 0 {
		return m
	}
	if len(m.semanticSpans) > 0 {
		shifted := make(highlight.LineSpans, len(m.semanticSpans))
		for line, spans := range m.semanticSpans {
			if line < atLine {
				shifted[line] = spans
				continue
			}
			newLine := line + delta
			if newLine < atLine {
				continue // this line's content was deleted
			}
			shifted[newLine] = append(shifted[newLine], spans...)
		}
		m.semanticSpans = shifted
	}
	if len(m.inlayHints) > 0 {
		shifted := make([]ClientInlayHint, 0, len(m.inlayHints))
		for _, h := range m.inlayHints {
			if h.Line >= atLine {
				newLine := h.Line + delta
				if newLine < atLine {
					continue // this line's content was deleted
				}
				h.Line = newLine
			}
			shifted = append(shifted, h)
		}
		m.inlayHints = shifted
	}
	return m
}

// scheduleLSPOverlayRefresh debounces a re-fetch of semantic tokens/inlay
// hints after an edit, without touching the current (possibly now slightly
// stale) cached data — it stays on screen until the fresh fetch replaces it.
// This mirrors VS Code's own approach: it doesn't blank decorations while
// waiting for an update, it swaps them in when ready. An earlier version of
// this cleared stale entries immediately for positional correctness, but
// since tree-sitter doesn't capture plain identifiers at all in most
// grammars, that meant those identifiers flashed to the terminal's default
// (often white) color on every keystroke — worse than the brief
// mispositioning it prevented. The render-side bounds check in
// renderLineChunk (skipping a span whose start no longer fits the line) keeps
// a stale span from ever painting past where it belongs, so leaving it on
// screen a little longer is safe. Debounced (not immediate) so a burst of
// keystrokes coalesces into one LSP round trip, mirroring the completion
// auto-trigger's debounce.
func (m Model) scheduleLSPOverlayRefresh() (Model, tea.Cmd) {
	m.lspOverlaySeq++
	seq := m.lspOverlaySeq
	cmd := tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
		return lspOverlayRefreshMsg{seq: seq}
	})
	return m, cmd
}

// ApplyExternalOps applies ops (e.g. a delete+insert replace pair from a
// workspace search-and-replace) as a single undoable action, through the same
// path as any other multi-op edit.
func (m Model) ApplyExternalOps(ops []document.Op) (Model, tea.Cmd) {
	return applyBatch(m, ops)
}

// applyBatch applies a slice of ops as a single undoable action.
// Inverses are computed before each apply, so the ops must not share lines.
// Always emits an EditRecordMsg so the App can update the jump list.
func applyBatch(m Model, ops []document.Op) (Model, tea.Cmd) {
	if len(ops) == 0 {
		return m, nil
	}
	before := m.cursorSnap()
	inverses := make([]document.Op, len(ops))
	sendCmds := make([]tea.Cmd, 0, len(ops))
	atLine, delta := -1, 0
	for i, op := range ops {
		inverses[i] = inverseOp(m, op) // must be before Apply
		sendCmds = append(sendCmds, m.sendOp(op))
		al, d := opLineDelta(op)
		if atLine < 0 || al < atLine {
			atLine = al
		}
		delta += d
	}
	if atLine < 0 {
		atLine = 0
	}
	m.undoStack = append(m.undoStack, undoEntry{ops: inverses, before: before})
	m.redoStack = nil
	fp, line, col := m.filePath, m.cursor.Line, m.cursor.Col
	depth := len(m.undoStack)
	recordCmd := func() tea.Msg {
		return EditRecordMsg{FilePath: fp, Line: line, Col: col, AtLine: atLine, LineDelta: delta, UndoDepth: depth}
	}
	m = m.shiftLSPOverlayLines(atLine, delta)
	m, refreshCmd := m.scheduleLSPOverlayRefresh()
	extraCmds := []tea.Cmd{refreshCmd}
	// sendOp already applied every op to the local buffer above, in order,
	// synchronously — that part was always correct. What's sequenced here
	// is each op's *network* send to the server: ops in a batch are often
	// position-dependent (e.g. a delete followed by an insert at the same
	// spot, the common shape for every LSP-edit/search-replace apply), so
	// the later op is only valid once the server has actually applied the
	// earlier one. tea.Batch runs commands concurrently with no ordering
	// guarantee — if the insert's RPC call reached the server before the
	// delete's, the delete would then fire with coordinates that no longer
	// point at the intended text, silently corrupting the buffer. Only the
	// op sends need this guarantee, so reparseHighlight/recordCmd — which
	// don't depend on send order — still run concurrently with each other
	// once the sends finish.
	// Same reasoning as applyOp: the caller already positioned m.cursor for
	// the post-edit state, so re-sync the viewport here rather than at
	// every applyBatch call site.
	m.scrollToCursor()
	tail := append([]tea.Cmd{m.reparseHighlight(), recordCmd}, extraCmds...)
	return m, tea.Sequence(append(sendCmds, tea.Batch(tail...))...)
}

// inverseOp returns the op that reverses op.
// For OpInsert the inverse is an OpDelete of the same span.
// For OpDelete the inverse is an OpInsert of the text that was there.
// The buffer must NOT yet have op applied when this is called.
func inverseOp(m Model, op document.Op) document.Op {
	switch op.Type {
	case document.OpInsert:
		toLine, toCol := insertEndPos(op.InsertLine, op.InsertCol, op.InsertText)
		return document.Op{
			Type:     document.OpDelete,
			FromLine: op.InsertLine,
			FromCol:  op.InsertCol,
			ToLine:   toLine,
			ToCol:    toCol,
		}
	case document.OpDelete:
		return document.Op{
			Type:       document.OpInsert,
			InsertLine: op.FromLine,
			InsertCol:  op.FromCol,
			InsertText: bufText(m, op.FromLine, op.FromCol, op.ToLine, op.ToCol),
		}
	}
	return document.Op{Type: document.OpNoop}
}

// insertEndPos returns the buffer position immediately after inserting text
// starting at (fromLine, fromCol).
func insertEndPos(fromLine, fromCol int, text string) (toLine, toCol int) {
	toLine, toCol = fromLine, fromCol
	for _, r := range text {
		if r == '\n' {
			toLine++
			toCol = 0
		} else {
			toCol++
		}
	}
	return
}

// bufText extracts the text in [fromLine:fromCol, toLine:toCol) from the buffer.
func bufText(m Model, fromLine, fromCol, toLine, toCol int) string {
	if fromLine == toLine {
		runes := []rune(m.buf.Line(fromLine))
		end := min(toCol, len(runes))
		start := min(fromCol, end)
		return string(runes[start:end])
	}
	var sb strings.Builder
	first := []rune(m.buf.Line(fromLine))
	if fromCol <= len(first) {
		sb.WriteString(string(first[fromCol:]))
	}
	sb.WriteByte('\n')
	for l := fromLine + 1; l < toLine; l++ {
		sb.WriteString(m.buf.Line(l))
		sb.WriteByte('\n')
	}
	last := []rune(m.buf.Line(toLine))
	sb.WriteString(string(last[:min(toCol, len(last))]))
	return sb.String()
}

func (m Model) fetchUpdates() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ops, ver, savedHash, generation, err := m.rpc.GetUpdates(ctx, m.bufID, m.version)
		if err != nil {
			return nil
		}
		// Deliver even with zero ops: savedHash keeps the dirty marker
		// accurate when another client (e.g. an agent) saves this buffer.
		return updatesMsg{ops: ops, version: ver, savedHash: savedHash, generation: generation}
	}
}

// saveAsPromptMsg triggers the save-as command prompt in Update.
type saveAsPromptMsg struct{ thenClose bool }

// doSave formats first (when format_on_save is enabled) then saves.
// For untitled buffers it triggers the save-as prompt instead.
func (m Model) doSave() tea.Cmd {
	if m.filePath == "" {
		return func() tea.Msg { return saveAsPromptMsg{} }
	}
	if m.cfg != nil && m.cfg.FormatOnSave {
		return m.fetchFormat(true)
	}
	return m.doSaveNow()
}

// doSaveNow writes the buffer to disk unconditionally.
func (m Model) doSaveNow() tea.Cmd {
	// m.buf is a *document.Buffer shared with the live model — capture its
	// version now, synchronously, rather than inside the closure below: the
	// closure runs after the RPC returns (up to 5s later), by which point
	// m.buf.Version() would reflect whatever the buffer's *current* version
	// is, not the version that was actually sent to the server. Capturing it
	// here is what makes the handler's later comparison meaningful.
	startVersion := m.buf.Version()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.Save(ctx, m.bufID); err != nil {
			return saveFailedMsg{bufID: m.bufID, err: err}
		}
		return savedMsg{bufID: m.bufID, version: startVersion}
	}
}

// doSaveAsNow writes the buffer to newPath via the server.
func (m Model) doSaveAsNow(newPath string, thenClose bool) tea.Cmd {
	startVersion := m.buf.Version() // see doSaveNow's comment on capturing this synchronously
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.SaveAs(ctx, m.bufID, newPath); err != nil {
			return saveFailedMsg{bufID: m.bufID, err: err}
		}
		return savedAsMsg{bufID: m.bufID, version: startVersion, newPath: newPath, thenClose: thenClose}
	}
}

// reparseHighlight schedules a fresh syntax-highlight pass and, via
// Model.hlSeq, stamps it as the newest outstanding request — see hlSeq's
// doc comment for why a highlightMsg needs this to avoid a slow, superseded
// parse (e.g. one still in flight when ":set ft=" swaps the highlighter)
// clobbering a later, correct result.
func (m Model) reparseHighlight() tea.Cmd {
	if m.hlr == nil {
		return nil
	}
	var seq uint64
	if m.hlSeq != nil {
		*m.hlSeq++
		seq = *m.hlSeq
	}
	content := []byte(m.buf.Content())
	hlr := m.hlr
	bracketColors := m.cfg != nil && m.cfg.BracketColors
	return func() tea.Msg {
		start := time.Now()
		spans := hlr.Highlight(content)
		if bracketColors {
			// Prepend bracket spans so they win over punctuation.bracket.
			for ln, bs := range highlight.BracketSpans(content) {
				spans[ln] = append(bs, spans[ln]...)
			}
		}
		return highlightMsg{spans: spans, duration: time.Since(start), seq: seq}
	}
}

// doCloseBuffer tells the server this client is done with this buffer,
// then signals the App to remove it from the buffer list.
func (m Model) doCloseBuffer() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.CloseBuffer(ctx, m.bufID) //nolint:errcheck
		return CloseBufferMsg{}
	}
}

// doSaveAndClose saves the buffer, then closes it.
func (m Model) doSaveAndClose() tea.Cmd {
	if m.filePath == "" {
		return func() tea.Msg { return saveAsPromptMsg{thenClose: true} }
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.Save(ctx, m.bufID); err != nil {
			return errorMsg{err}
		}
		m.rpc.CloseBuffer(ctx, m.bufID) //nolint:errcheck
		return CloseBufferMsg{}
	}
}
