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

	if m.snippetOn {
		switch msg.String() {
		case "tab":
			return m.snippetJump(1), nil
		case "shift+tab":
			return m.snippetJump(-1), nil
		case "backspace":
			return m.snippetEdit(msg)
		default:
			if len(msg.Runes) > 0 {
				return m.snippetEdit(msg)
			}
			// Esc, cursor movement, Enter, etc. leave snippet mode and are then
			// handled normally — so a single Esc also leaves insert mode rather
			// than needing a second press.
			m = m.exitSnippet()
		}
	}
	return m.handleInsertKey(msg)
}

// enterSnippet activates snippet mode over stops (absolute columns on line) and
// selects the first stop.
func (m Model) enterSnippet(line int, stops []snippetStop) Model {
	m.snippetOn = true
	m.snippetLine = line
	m.snippetStops = stops
	return m.selectSnippetStop(0)
}

// exitSnippet clears snippet mode and any placeholder selection.
func (m Model) exitSnippet() Model {
	m.snippetOn = false
	m.snippetStops = nil
	m.snippetIdx = 0
	m.sel = nil
	return m
}

// selectSnippetStop makes stop idx active: a non-empty placeholder is selected
// (highlighted) so typing replaces it; an empty stop just places the cursor.
func (m Model) selectSnippetStop(idx int) Model {
	m.snippetIdx = idx
	s := m.snippetStops[idx]
	if s.end > s.start {
		m.sel = &Selection{
			Anchor: document.Pos{Line: m.snippetLine, Col: s.start},
			Head:   document.Pos{Line: m.snippetLine, Col: s.end - 1}, // selection end is inclusive
		}
		m.cursor = document.Pos{Line: m.snippetLine, Col: s.start}
	} else {
		m.sel = nil
		m.cursor = document.Pos{Line: m.snippetLine, Col: s.start}
	}
	return m
}

// snippetJump moves to the next/previous tab stop, leaving snippet mode when
// advancing past the last stop.
func (m Model) snippetJump(dir int) tea.Model {
	next := m.snippetIdx + dir
	if next < 0 {
		next = 0
	}
	if next >= len(m.snippetStops) {
		return m.exitSnippet()
	}
	return m.selectSnippetStop(next)
}

// snippetEdit handles a printable key or backspace while in snippet mode: it
// replaces the selected placeholder (if any) with the keypress, then keeps the
// remaining stops positioned by the net change in the line's length. Leaving the
// snippet line abandons snippet mode.
func (m Model) snippetEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	idx := m.snippetIdx
	line := m.snippetLine
	before := m.buf.LineLen(line)
	editCol := m.cursor.Col

	var cmds []tea.Cmd
	if m.sel != nil {
		start, _ := m.sel.ordered()
		editCol = start.Col
		m2, delCmd := m.deleteSelection()
		m = m2
		if delCmd != nil {
			cmds = append(cmds, delCmd)
		}
	}
	model, editCmd := m.handleInsertKey(msg)
	m = model.(Model)
	if editCmd != nil {
		cmds = append(cmds, editCmd)
	}

	if m.cursor.Line != line { // edit inserted a newline / moved off-line
		return m.exitSnippet(), tea.Sequence(cmds...)
	}

	// Shift the stops after the active one by the net column change at editCol,
	// and grow/shrink the active stop's end to match.
	delta := m.buf.LineLen(line) - before
	stops := make([]snippetStop, len(m.snippetStops))
	copy(stops, m.snippetStops)
	m.snippetStops = stops
	if idx < len(m.snippetStops) {
		m.snippetStops[idx].end += delta
	}
	for j := idx + 1; j < len(m.snippetStops); j++ {
		if m.snippetStops[j].start >= editCol {
			m.snippetStops[j].start += delta
			m.snippetStops[j].end += delta
		}
	}
	return m, tea.Sequence(cmds...)
}

// insertCmds is the flat (non-nested) set of Insert-mode key bindings; unlike
// prefixCmds there are no multi-key sequences, so lookup is a single findIn
// call. Any key not listed here falls through to insertSelfInsert.
var insertCmds = []command{
	// ctrl+space sends NUL → "ctrl+@" in most terminals.
	{key: "ctrl+@", name: "trigger-completion", label: "Trigger completion", execute: executeTriggerCompletion},
	{key: "ctrl+space", name: "trigger-completion", label: "Trigger completion", execute: executeTriggerCompletion},
	{key: "esc", name: "exit-insert-mode", label: "Exit insert mode", execute: executeInsertEsc},
	// Escape to normal mode (vim convention) — never close from insert mode.
	{key: "ctrl+c", name: "exit-insert-mode", label: "Exit insert mode", execute: executeInsertCtrlC},
	{key: "ctrl+s", name: "save", label: "Save", execute: executeSave},
	{key: "backspace", name: "backspace", label: "Backspace", execute: executeInsertBackspace},
	{key: "delete", name: "delete-forward", label: "Delete forward", execute: executeInsertDelete},
	{key: "enter", name: "newline", label: "Newline", execute: executeInsertEnter},
	{key: "tab", name: "insert-tab", label: "Insert tab", execute: executeInsertTab},
	{key: "left", name: "cursor-left", label: "Cursor left", execute: executeInsertMoveLeft},
	{key: "right", name: "cursor-right", label: "Cursor right", execute: executeInsertMoveRight},
	{key: "up", name: "cursor-up", label: "Cursor up", execute: executeInsertMoveUp},
	{key: "down", name: "cursor-down", label: "Cursor down", execute: executeInsertMoveDown},
	{key: "home", name: "line-start", label: "Line start", execute: executeInsertHome},
	{key: "end", name: "line-end", label: "Line end", execute: executeInsertEnd},
}

// handleInsertKey handles a single insert-mode key edit. Completion-popup
// bookkeeping lives in handleInsert; this function only edits the buffer.
func (m Model) handleInsertKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := findIn(insertCmds, []string{msg.String()}); ok && cmd.execute != nil {
		return cmd.execute(m)
	}
	return m.insertSelfInsert(msg)
}

func executeTriggerCompletion(m Model) (tea.Model, tea.Cmd) {
	m.completionPrefix = m.currentWordPrefix()
	return m, m.fetchCompletions()
}

func executeInsertEsc(m Model) (tea.Model, tea.Cmd) {
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
}

func executeInsertCtrlC(m Model) (tea.Model, tea.Cmd) {
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
}

func executeInsertBackspace(m Model) (tea.Model, tea.Cmd) {
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
	return m, nil
}

func executeInsertDelete(m Model) (tea.Model, tea.Cmd) {
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
	return m, nil
}

func executeInsertEnter(m Model) (tea.Model, tea.Cmd) {
	if len(m.extraCursors) > 0 {
		return applyInsertToAllCursors(m, "\n")
	}
	return m.handleEnter()
}

func executeInsertTab(m Model) (tea.Model, tea.Cmd) {
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
}

func executeInsertMoveLeft(m Model) (tea.Model, tea.Cmd) {
	m.moveCursor(0, -1)
	return m, nil
}

func executeInsertMoveRight(m Model) (tea.Model, tea.Cmd) {
	m.moveCursor(0, 1)
	return m, nil
}

func executeInsertMoveUp(m Model) (tea.Model, tea.Cmd) {
	m.moveCursor(-1, 0)
	return m, nil
}

func executeInsertMoveDown(m Model) (tea.Model, tea.Cmd) {
	m.moveCursor(1, 0)
	return m, nil
}

func executeInsertHome(m Model) (tea.Model, tea.Cmd) {
	m.cursor.Col = 0
	return m, nil
}

func executeInsertEnd(m Model) (tea.Model, tea.Cmd) {
	m.cursor.Col = m.buf.LineLen(m.cursor.Line)
	return m, nil
}

// insertSelfInsert handles any key not bound in insertCmds: it inserts the
// typed rune(s) into the buffer, applying auto-pairing, signature-help, and
// completion triggers along the way.
func (m Model) insertSelfInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(msg.Runes) == 0 {
		return m, nil
	}
	text := string(msg.Runes)
	if len(m.extraCursors) == 0 && strings.Contains(text, "\n") {
		return m.insertPastedText(text)
	}
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
			if r == '{' && m.shouldExpandBraceBlock() {
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
	if !m2.snippetOn && (r == '.' || (isWordChar(r) && !m2.completionOn)) {
		m2.completionSeq++
		seq := m2.completionSeq
		delayed := tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg {
			return triggerCompletionMsg{seq: seq}
		})
		return m2, tea.Batch(cmd, delayed)
	}
	return m2, cmd
}

// insertPastedText inserts multi-line text (from a terminal paste or an
// IME multi-rune commit) at the cursor, reindenting every line after the
// first to match the destination context — the same rule handleEnter uses
// for a freshly typed line there — while preserving the pasted lines'
// indentation relative to each other. The first line is left untouched
// since it continues whatever's already before the cursor.
func (m Model) insertPastedText(text string) (tea.Model, tea.Cmd) {
	lines := strings.Split(text, "\n")
	rest := lines[1:]
	if baseline, ok := blockBaseIndent(rest); ok {
		target := m.contextIndent(m.buf.Line(m.cursor.Line), m.cursor.Line, m.cursor.Col)
		rest, _ = reindentLines(rest, baseline, target)
	}

	op := document.Op{
		ClientID:   m.rpc.ClientID(),
		Type:       document.OpInsert,
		InsertLine: m.cursor.Line,
		InsertCol:  m.cursor.Col,
		InsertText: strings.Join(append([]string{lines[0]}, rest...), "\n"),
	}
	lastLine := lines[0]
	if len(rest) > 0 {
		lastLine = rest[len(rest)-1]
	}
	m.cursor = document.Pos{Line: m.cursor.Line + len(lines) - 1, Col: len([]rune(lastLine))}
	return applyOp(m, op)
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

// signatureIsCallable reports whether a resolved completion detail describes a
// callable symbol, so call parentheses are inserted even when the LSP kind
// doesn't say so. An already-imported function comes back as kind Variable
// with detail like "(alias) function greetLoudly(name: string): string", and a
// function-typed value as "const cb: (x: number) => void". A not-yet-imported
// auto-import candidate instead leads with a preamble line — detail is
// "Auto import from './helper'\nfunction bar(x: number, y: number): number" —
// so every line is checked, not just the first.
func signatureIsCallable(detail string) bool {
	for _, line := range strings.Split(detail, "\n") {
		switch {
		case strings.Contains(line, "function "):
			return true
		case strings.Contains(line, "(method)"):
			return true
		case strings.Contains(line, ") => "):
			// Arrow function type, e.g. `(x: number) => void` — callable. But a
			// construct signature (`new (...) => T`, `abstract new (...) => T`) is
			// new-able, not directly callable, so don't append call parens for it.
			typ := line
			if i := strings.Index(typ, ": "); i >= 0 {
				typ = typ[i+2:]
			}
			typ = strings.TrimSpace(typ)
			if !strings.HasPrefix(typ, "new (") && !strings.HasPrefix(typ, "abstract new (") {
				return true
			}
		}
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
		// A server's textEdit is computed once, at fetch time, against however
		// much of the prefix was typed then — it does not track further typing,
		// because indigo's incremental narrowing (see refreshCompletionFilter)
		// deliberately re-filters the cached list locally instead of
		// re-fetching on every keystroke. Confirmed against real gopls: the
		// same candidate's textEdit.range end sits exactly at the trigger
		// column right after the fetch, but at the full typed-prefix column
		// when fetched fresh later — so if the cursor has since moved past the
		// edit's end (more was typed after this item was fetched), extend the
		// edit to also consume those characters. Otherwise they'd survive
		// untouched right after the inserted text (e.g. accepting a
		// zero-width edit fetched right after '.' but accepted after typing
		// "sn" produced "m.snippetOnsn" instead of "m.snippetOn"). This must
		// NOT fire when the edit legitimately extends past the cursor into
		// pre-existing buffer text (the mid-word-completion case), which is
		// exactly the pToCol <= at.Col direction below.
		if pToLine == at.Line && at.Col > pToCol {
			pToCol = at.Col
		}
	}
	if pFromCol < 0 {
		pFromCol = 0
	}

	// For functions/methods/constructors, also insert the call parentheses and
	// leave the cursor between them, then trigger signature help so the
	// parameters are visible. Skipped when the text already has a '(' or one
	// already follows the edit, to avoid doubling.
	addParens := (completionAddsParens(item.Kind) || signatureIsCallable(item.Detail)) &&
		!strings.ContainsRune(baseText, '(') &&
		!strings.Contains(baseText, "\n") &&
		!m.bufCharAfterIs(document.Pos{Line: pToLine, Col: pToCol}, '(')
	// paramStart is the rune offset of the first argument placeholder within
	// baseText, or -1 when there are no named parameters (bare "()").
	paramStart := -1
	var stopOffsets []snippetStop // tab stops as rune offsets within baseText
	if addParens {
		if names := parseSignatureParams(item.Detail); len(names) > 0 {
			var b strings.Builder
			b.WriteString(baseText)
			b.WriteByte('(')
			off := len([]rune(baseText)) + 1 // just past the '('
			for i, n := range names {
				if i > 0 {
					b.WriteString(", ")
					off += 2
				}
				nl := len([]rune(n))
				stopOffsets = append(stopOffsets, snippetStop{off, off + nl})
				b.WriteString(n)
				off += nl
			}
			b.WriteByte(')')
			stopOffsets = append(stopOffsets, snippetStop{off + 1, off + 1}) // final stop after ')'
			baseText = b.String()
			paramStart = stopOffsets[0].start
		} else {
			baseText += "()"
		}
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
		switch {
		case paramStart >= 0:
			col = pFromCol + paramStart // start of the first argument placeholder
		case addParens:
			col-- // sit between the empty ( and )
		}
		m2.cursor = document.Pos{Line: pFromLine + lineDelta, Col: col}
	}
	m2.scrollToCursor()
	// Enter snippet mode over the argument placeholders (converting the
	// baseText-relative offsets to absolute columns on the inserted line).
	if len(stopOffsets) > 0 {
		line := pFromLine + lineDelta
		abs := make([]snippetStop, len(stopOffsets))
		for i, s := range stopOffsets {
			abs[i] = snippetStop{pFromCol + s.start, pFromCol + s.end}
		}
		m2 = m2.enterSnippet(line, abs)
	}
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
