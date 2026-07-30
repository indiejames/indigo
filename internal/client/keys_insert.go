package client

import (
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// handleInsert dispatches an insert-mode key, managing the completion popup
// (navigation, incremental narrowing, dismissal) before delegating the actual
// edit to handleInsertKey.
func (m Model) handleInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.completionOn {
		switch msg.String() {
		case "enter", "tab":
			return m.applyCompletion()
		case "down", "ctrl+n":
			m.completionIdx = (m.completionIdx + 1) % len(m.completions)
			return m, nil
		case "up", "shift+tab", "ctrl+p":
			m.completionIdx = (m.completionIdx - 1 + len(m.completions)) % len(m.completions)
			return m, nil
		case "esc":
			return m.clearedCompletion(), nil
		}
		// Word characters and backspace narrow the open list instead of
		// dismissing it: apply the edit normally, then re-filter the cached full
		// list against the new prefix (no re-fetch — the list is complete).
		if completionContinues(msg) {
			model, cmd := m.handleInsertKey(msg)
			return model.(Model).refreshCompletionFilter(), cmd
		}
		// Any other key dismisses the popup, then is handled normally.
		m = m.clearedCompletion()
	}
	return m.handleInsertKey(msg)
}

// handleInsertKey handles a single insert-mode key edit. Completion-popup
// bookkeeping lives in handleInsert; this function only edits the buffer.
func (m Model) handleInsertKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+@", "ctrl+space": // ctrl+space sends NUL → "ctrl+@" in most terminals
		m.completionPrefix = m.currentWordPrefix()
		return m, m.fetchCompletions()

	case "esc":
		m.mode = ModeNormal
		m.sigHelp = nil
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		// Commit the undo group accumulated during this Insert session.
		var recordCmd tea.Cmd
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
			fp := m.filePath
			atLine := minAffectedLine(m.currentGroup)
			lineDelta := m.buf.LineCount() - m.insertLineCount
			startLine := m.groupBefore.cursor.Line
			startCol := m.groupBefore.cursor.Col
			depth := len(m.undoStack)
			recordCmd = func() tea.Msg {
				return EditRecordMsg{
					FilePath:  fp,
					Line:      startLine,
					Col:       startCol,
					AtLine:    atLine,
					LineDelta: lineDelta,
					UndoDepth: depth,
				}
			}
		}
		m.currentGroup = nil
		return m, recordCmd

	case "ctrl+c":
		// Escape to normal mode (vim convention) — never close from insert mode.
		m.mode = ModeNormal
		m.sigHelp = nil
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
		}
		m.currentGroup = nil
		return m, nil

	case "ctrl+s":
		return m, m.doSave()

	case "backspace":
		if len(m.extraCursors) > 0 {
			return applyBackspaceToAllCursors(m)
		}
		if m.cursor.Col > 0 {
			toCol := m.cursor.Col
			if closer, ok := autoPairs[m.charBeforeCursor()]; ok && m.charAfterCursor() == closer {
				toCol++ // also delete the auto-inserted closer right after the cursor
			}
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col - 1,
				ToLine:   m.cursor.Line,
				ToCol:    toCol,
			}
			m.cursor.Col--
			return applyOp(m, op)
		} else if m.cursor.Line > 0 {
			prevLen := m.buf.LineLen(m.cursor.Line - 1)
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line - 1,
				FromCol:  prevLen,
				ToLine:   m.cursor.Line,
				ToCol:    0,
			}
			m.cursor = document.Pos{Line: m.cursor.Line - 1, Col: prevLen}
			return applyOp(m, op)
		}

	case "delete":
		lineLen := m.buf.LineLen(m.cursor.Line)
		if m.cursor.Col < lineLen {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col + 1,
			}
			return applyOp(m, op)
		} else if m.cursor.Line < m.buf.LineCount()-1 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  lineLen,
				ToLine:   m.cursor.Line + 1,
				ToCol:    0,
			}
			return applyOp(m, op)
		}

	case "enter":
		if len(m.extraCursors) > 0 {
			return applyInsertToAllCursors(m, "\n")
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: 0}
		return applyOp(m, op)

	case "tab":
		if len(m.extraCursors) > 0 {
			return applyInsertToAllCursors(m, "\t")
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\t",
		}
		m.cursor.Col++
		return applyOp(m, op)

	case "left":
		m.moveCursor(0, -1)
	case "right":
		m.moveCursor(0, 1)
	case "up":
		m.moveCursor(-1, 0)
	case "down":
		m.moveCursor(1, 0)
	case "home":
		m.cursor.Col = 0
	case "end":
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	default:
		if len(msg.Runes) > 0 {
			text := string(msg.Runes)
			if len(m.extraCursors) > 0 {
				m2, cmd := applyInsertToAllCursors(m, text)
				r := msg.Runes[0]
				if r == '(' || r == ',' {
					return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
				}
				if r == ')' {
					m2.sigHelp = nil
				}
				return m2, cmd
			}

			r := msg.Runes[0]

			// Typing a closer that's already the next character just moves
			// past it, instead of inserting a duplicate.
			if len(msg.Runes) == 1 && autoPairClosers[r] && m.charAfterCursor() == r {
				m.cursor.Col++
				if r == ')' {
					m.sigHelp = nil
				}
				return m, nil
			}

			// Typing an opener auto-inserts its closer, leaving the cursor
			// between the two. '{' additionally expands onto its own
			// indented line, since braces almost always open a block.
			if len(msg.Runes) == 1 {
				if closer, ok := autoPairs[r]; ok && m.shouldAutoPair(r) {
					if r == '{' {
						return m.insertBraceBlock()
					}
					op := document.Op{
						ClientID:   m.rpc.ClientID(),
						Type:       document.OpInsert,
						InsertLine: m.cursor.Line,
						InsertCol:  m.cursor.Col,
						InsertText: string(r) + string(closer),
					}
					m.cursor.Col++
					m2, cmd := applyOp(m, op)
					if r == '(' {
						return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
					}
					return m2, cmd
				}
			}

			op := document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: m.cursor.Line,
				InsertCol:  m.cursor.Col,
				InsertText: text,
			}
			m.cursor.Col += len(msg.Runes)
			m2, cmd := applyOp(m, op)
			// Auto-trigger sig help on '(' or ','.
			if r == '(' || r == ',' {
				return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
			}
			// Close sig help on ')'.
			if r == ')' {
				m2.sigHelp = nil
			}
			// Auto-trigger completions on '.' (member access) or while typing an
			// identifier when the popup isn't already open. Delayed so DidChange
			// reaches the LSP server before Complete does; the seq token cancels
			// all but the latest pending trigger, so a burst of keystrokes causes
			// a single fetch. Once the popup is open, further typing narrows the
			// cached list (handleInsert) rather than re-fetching.
			if r == '.' || (isWordChar(r) && !m2.completionOn) {
				m2.completionSeq++
				seq := m2.completionSeq
				delayed := tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
					return triggerCompletionMsg{seq: seq}
				})
				return m2, tea.Batch(cmd, delayed)
			}
			return m2, cmd
		}
	}
	return m, nil
}

// completionContinues reports whether msg should narrow an open completion
// popup (a typed word character or backspace) rather than dismiss it.
func completionContinues(msg tea.KeyMsg) bool {
	if msg.String() == "backspace" {
		return true
	}
	return len(msg.Runes) == 1 && isWordChar(msg.Runes[0])
}

// completionAddsParens reports whether accepting a completion of the given LSP
// kind should also insert call parentheses (Method=2, Function=3, Constructor=4).
func completionAddsParens(kind uint8) bool {
	switch kind {
	case 2, 3, 4:
		return true
	}
	return false
}

// bufCharAfterIs reports whether the character immediately after position at is ch.
func (m Model) bufCharAfterIs(at document.Pos, ch rune) bool {
	if at.Line < 0 || at.Line >= m.buf.LineCount() {
		return false
	}
	line := []rune(m.buf.Line(at.Line))
	return at.Col >= 0 && at.Col < len(line) && line[at.Col] == ch
}

// clearedCompletion returns m with all completion-popup state reset.
func (m Model) clearedCompletion() Model {
	m.completionOn = false
	m.completions = nil
	m.completionsRaw = nil
	return m
}

// refreshCompletionFilter recomputes the visible completion list from the cached
// full list against the current word prefix, after an edit narrowed or widened
// it. Dismisses the popup when nothing matches anymore.
func (m Model) refreshCompletionFilter() Model {
	if !m.completionOn {
		return m
	}
	m.completionPrefix = m.currentWordPrefix()
	m.completions = filterCompletions(m.completionsRaw, m.completionPrefix)
	if len(m.completions) == 0 {
		return m.clearedCompletion()
	}
	if m.completionIdx >= len(m.completions) {
		m.completionIdx = 0
	}
	return m
}

// applyCompletion accepts the selected completion item. If the item carries a
// resolve token but no auto-import edits yet, it's resolved first (async) so the
// import line comes back before the edit is applied; otherwise it's applied
// immediately. The cursor position and typed prefix are captured now so the
// deferred apply is unaffected by any later cursor movement.
func (m Model) applyCompletion() (tea.Model, tea.Cmd) {
	if !m.completionOn || len(m.completions) == 0 {
		m.completionOn = false
		return m, nil
	}
	item := m.completions[m.completionIdx]
	at := m.cursor
	prefix := m.completionPrefix
	m.completionOn = false
	m.completions = nil

	// additionalTextEdits (the auto-import line) are computed lazily by the
	// server; resolve the accepted item first when it has a resolve token but no
	// edits yet. See RPC.ResolveCompletion.
	if len(item.AdditionalEdits) == 0 && len(item.Data) > 0 {
		return m, m.resolveCompletionCmd(item, at, prefix)
	}
	return m.applyCompletionItem(item, at, prefix)
}

// applyCompletionItem applies an accepted completion: it replaces the typed
// prefix with the completion text and applies any additionalTextEdits (the
// auto-import line) as a single undoable action. Edits are applied bottom-to-top
// so earlier positions aren't shifted; the cursor is then placed at the end of
// the inserted text, moved down by any whole lines the import edits inserted
// above it.
func (m Model) applyCompletionItem(item ClientCompletion, at document.Pos, prefix string) (tea.Model, tea.Cmd) {
	baseText := item.InsertText
	if baseText == "" {
		baseText = item.Label
	}

	// Primary edit range: prefer the server-provided textEdit, which is
	// authoritative and may cover more than the typed prefix — notably the whole
	// identifier when completing mid-word (the prefix heuristic would only
	// replace the part before the cursor, leaving the suffix behind). Otherwise
	// replace the typed prefix immediately before the cursor.
	pFromLine, pFromCol := at.Line, at.Col-len([]rune(prefix))
	pToLine, pToCol := at.Line, at.Col
	if item.TextEdit != nil {
		pFromLine, pFromCol = item.TextEdit.FromLine, item.TextEdit.FromCol
		pToLine, pToCol = item.TextEdit.ToLine, item.TextEdit.ToCol
		baseText = item.TextEdit.NewText
	}
	if pFromCol < 0 {
		pFromCol = 0
	}

	// For functions/methods/constructors, also insert the call parentheses and
	// leave the cursor between them, then trigger signature help so the
	// parameters are visible. Skipped when the text already has a '(' or one
	// already follows the edit, to avoid doubling.
	addParens := completionAddsParens(item.Kind) &&
		!strings.ContainsRune(baseText, '(') &&
		!strings.Contains(baseText, "\n") &&
		!m.bufCharAfterIs(document.Pos{Line: pToLine, Col: pToCol}, '(')
	if addParens {
		baseText += "()"
	}

	// Primary edit plus the additionalTextEdits — the auto-import line(s), which
	// sit above the cursor.
	edits := make([]ClientLspEdit, 0, 1+len(item.AdditionalEdits))
	edits = append(edits, ClientLspEdit{
		FromLine: pFromLine, FromCol: pFromCol,
		ToLine: pToLine, ToCol: pToCol,
		NewText: baseText,
	})
	edits = append(edits, item.AdditionalEdits...)

	m2, cmd := m.applyCompletionEdits(edits)

	// Place the cursor at the end of the inserted text, shifted down by whole
	// lines any additional edit inserted at or above the primary edit's start.
	// Auto-import edits are whole-line insertions above the cursor, so only the
	// line offset changes; the column is the completion's end on its own line.
	lineDelta := 0
	for _, e := range item.AdditionalEdits {
		if e.FromLine < pFromLine || (e.FromLine == pFromLine && e.FromCol <= pFromCol) {
			lineDelta += strings.Count(e.NewText, "\n") - (e.ToLine - e.FromLine)
		}
	}
	if nl := strings.Count(baseText, "\n"); nl > 0 {
		last := baseText[strings.LastIndex(baseText, "\n")+1:]
		m2.cursor = document.Pos{Line: pFromLine + lineDelta + nl, Col: len([]rune(last))}
	} else {
		col := pFromCol + len([]rune(baseText))
		if addParens {
			col-- // sit between the inserted ( and )
		}
		m2.cursor = document.Pos{Line: pFromLine + lineDelta, Col: col}
	}
	m2.scrollToCursor()
	if addParens {
		return m2, tea.Batch(cmd, m2.fetchSignatureHelp())
	}
	return m2, cmd
}

// applyCompletionEdits applies a completion's edits through applyOp (so the
// inverses join the current Insert-session undo group rather than pushing a
// separate undo entry mid-session, which applyBatch would do). Edits are applied
// bottom-to-top so an edit never shifts the coordinates of one not yet applied,
// and the per-op sends are sequenced so a delete always reaches the server
// before the insert that reuses its position.
func (m Model) applyCompletionEdits(edits []ClientLspEdit) (Model, tea.Cmd) {
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].FromLine != edits[j].FromLine {
			return edits[i].FromLine > edits[j].FromLine
		}
		return edits[i].FromCol > edits[j].FromCol
	})
	var cmds []tea.Cmd
	for _, e := range edits {
		if e.FromLine != e.ToLine || e.FromCol != e.ToCol {
			del := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: e.FromLine, FromCol: e.FromCol,
				ToLine: e.ToLine, ToCol: e.ToCol,
			}
			var c tea.Cmd
			m, c = applyOp(m, del)
			cmds = append(cmds, c)
		}
		if e.NewText != "" {
			ins := document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: e.FromLine, InsertCol: e.FromCol,
				InsertText: e.NewText,
			}
			var c tea.Cmd
			m, c = applyOp(m, ins)
			cmds = append(cmds, c)
		}
	}
	return m, tea.Sequence(cmds...)
}
