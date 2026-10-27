package client

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// The editor is modeless: every key either types or runs a command, and there
// is no Normal mode to leave or enter. insertCmds is the whole keymap.
//
// The chords are chosen from what a Bubble Tea v2 program receives in Ghostty
// with no terminal or macOS settings changed (measured 2026-10-24):
//
//   - Option+←/→ arrive as alt+b/alt+f, the readline word motions, so those
//     are word movement. alt+left/alt+right are accepted too, for terminals
//     that send the arrow.
//   - Cmd+←/→ arrive as ctrl+a/ctrl+e, so those are line start and end, not
//     select-all and something else. Select all is ctrl+shift+a.
//   - Shift+Cmd+←/→ arrive as shift+meta+left/right, Shift+Option+←/→ as
//     alt+shift+left/right.
//   - Plain Ctrl+arrows never arrive: macOS keeps them for Mission Control.
//   - Shift+Enter arrives as alt+enter, so the two cannot mean different
//     things.

// typingPause ends a typing run: text typed after a pause this long is a new
// undo step. A var so a test can shorten it.
var typingPause = time.Second

// now is the clock beginTyping reads. A var so a test can control it.
var now = time.Now

// typingActions are the insertCmds entries that type: like plain characters,
// they join the open typing run, so one undo takes back a run of typing
// rather than one keystroke. Every other command ends the run first.
var typingActions = map[string]bool{
	"backspace":      true,
	"delete-forward": true,
	"newline":        true,
	"insert-tab":     true,
}

// beginTyping makes sure a typing run is open for the edit about to be made,
// ending the current one first if typing paused. The returned command records
// the run that ended, if any.
func (m Model) beginTyping() (Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.currentGroup != nil && now().Sub(m.lastTypedAt) > typingPause {
		m, cmd = m.endTyping()
	}
	if m.currentGroup == nil {
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
	}
	m.lastTypedAt = now()
	return m, cmd
}

// endTyping closes the open typing run into one undo entry. Every command that
// is not typing calls it first, so the undo stack keeps the order things
// happened in: an edit a command makes must not land below typing that came
// before it, which is where it would go if the run were pushed afterwards.
func (m Model) endTyping() (Model, tea.Cmd) {
	if m.currentGroup == nil {
		return m, nil
	}
	var cmd tea.Cmd
	if len(m.currentGroup) > 0 {
		m.undoStack = append(m.undoStack, undoEntry{ops: m.currentGroup, before: m.groupBefore})
		cmd = m.groupRecordCmd(m.currentGroup, m.groupBefore)
	}
	m.currentGroup = nil
	return m, cmd
}

// typeOverSelection deletes the selection before a typing edit, so typing
// replaces selected text. It reports whether there was one, because Backspace
// and Delete then have nothing more to do.
func (m Model) typeOverSelection() (Model, tea.Cmd, bool) {
	if len(m.extraCursors) > 0 {
		return m.typeOverCursorSelections()
	}
	if m.sel == nil {
		return m, nil, false
	}
	m2, cmd := m.deleteSelectionRaw()
	return m2, cmd, true
}

// typeOverCursorSelections is typeOverSelection with several cursors: each
// cursor's selection is deleted, so typing replaces every one of them.
func (m Model) typeOverCursorSelections() (Model, tea.Cmd, bool) {
	hadSel := m.sel != nil
	for _, ec := range m.extraCursors {
		hadSel = hadSel || ec.sel != nil
	}
	if !hadSel {
		return m, nil, false
	}
	// A cursor with no selection deletes nothing. deleteAllCursorSelections
	// deletes the character under such a cursor, so give it an empty
	// selection at the cursor instead.
	if m.sel == nil {
		m.sel = &Selection{Anchor: m.cursor, Head: m.cursor}
	}
	extras := make([]ExtraCursor, len(m.extraCursors))
	copy(extras, m.extraCursors)
	for i := range extras {
		if extras[i].sel == nil {
			extras[i].sel = &Selection{Anchor: extras[i].pos, Head: extras[i].pos}
		}
	}
	m.extraCursors = extras
	m2, cmd := deleteAllCursorSelections(m, false)
	return m2, cmd, true
}

// normalAction runs the action registered under name in the command catalog
// (prefixCmds), looked up when it runs rather than when this table is built,
// which would be an initialisation cycle. Most chords are new keys for actions
// the catalog already has.
func normalAction(name string) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		if fn, ok := actionRegistry(defaultPrefixCmds)[name]; ok {
			return fn(m)
		}
		return m.pushStatus("E: no action " + name), nil
	}
}

func sendMsg(msg tea.Msg) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		return m, func() tea.Msg { return msg }
	}
}

// executeModelessEsc closes whatever is open — popups, the selection, extra
// cursors, a search — and does nothing else. There is no mode to leave.
func executeModelessEsc(m Model) (tea.Model, tea.Cmd) {
	m.sigHelp = nil
	m = m.abandonCompletion()
	if m.snippetOn {
		m = m.exitSnippet()
	}
	m.sel = nil
	m.extraCursors = nil
	m.mark = nil
	m = m.withClearedSearch()
	return m, nil
}

func executeOpenSpaceMenu(m Model) (tea.Model, tea.Cmd) {
	m.prefixSeq = []string{"space"}
	return m, nil
}

// executeModelessClose is Ctrl+W: close this buffer, or ask first if it has
// unsaved changes.
func executeModelessClose(m Model) (tea.Model, tea.Cmd) {
	if m.buf.Dirty() {
		return m, func() tea.Msg { return ConfirmCloseMsg{} }
	}
	return m, m.doCloseBuffer()
}

// executeModelessLeft and executeModelessRight move one character, or, with a
// selection, collapse it to its start or end, as GUI editors do.
func executeModelessLeft(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		if m.sel != nil {
			m.cursor, _ = m.sel.ordered()
			m.sel = nil
			m.scrollToCursor()
			return
		}
		m.moveCursorChar(-1)
	})
	return m, nil
}

func executeModelessRight(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		if m.sel != nil {
			_, m.cursor = m.sel.ordered()
			m.sel = nil
			m.scrollToCursor()
			return
		}
		m.moveCursorChar(1)
	})
	return m, nil
}

// executeWordRight is Option+→: to the end of the word, as in macOS text
// fields, rather than the modal editor's start of the next word.
func executeWordRight(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.cursor = m.wordEndAfter(m.cursor)
		m.scrollToCursor()
	})
	return m, nil
}

// executeWordLeft is Option+←: to the start of the word.
func executeWordLeft(m Model) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		m.sel = nil
		m.cursor = m.wordStartBefore(m.cursor)
		m.scrollToCursor()
	})
	return m, nil
}

func executeExtendLineUp(m Model) (tea.Model, tea.Cmd) {
	return extendVertically(m, -1)
}

func executeExtendLineDown(m Model) (tea.Model, tea.Cmd) {
	return extendVertically(m, 1)
}

// extendVertically moves the selection's head a line up or down, starting a
// selection at the cursor if there is none.
func extendVertically(m Model, dLine int) (tea.Model, tea.Cmd) {
	m.applyToAllCursors(func(m *Model) {
		from := m.cursor
		m.moveCursor(dLine, 0)
		to := m.cursor
		m.cursor = from
		m.extendSelectionTo(to)
	})
	return m, nil
}

// executeModelessCopy is Ctrl+C. With nothing selected it copies the whole
// line, as GUI editors do; a selection stays selected.
func executeModelessCopy(m Model) (tea.Model, tea.Cmd) {
	if len(m.extraCursors) > 0 {
		return yankAllCursorSelections(m)
	}
	text := m.selectedText()
	what := "copied"
	if m.sel == nil {
		text = m.buf.Line(m.cursor.Line) + "\n"
		what = "copied line"
	}
	if err := clipboardWriter(text); err != nil {
		return m.pushStatus("clipboard: " + err.Error()), nil
	}
	return m.pushStatus(what), nil
}

// executeModelessCut is Ctrl+X. With nothing selected it cuts the whole line.
func executeModelessCut(m Model) (tea.Model, tea.Cmd) {
	if m.sel == nil && len(m.extraCursors) == 0 {
		m.selectLine()
	}
	return executeCutSelection(m)
}

// executeModelessPaste is Ctrl+V: paste at the cursor, replacing a selection,
// and leave the cursor after the pasted text. Replacing and pasting are one
// undo step.
func executeModelessPaste(m Model) (tea.Model, tea.Cmd) {
	text, err := clipboardReader()
	if err != nil {
		return m.pushStatus("clipboard: " + err.Error()), nil
	}
	text = document.NormalizeNewlines(text)
	if text == "" {
		return m, nil
	}
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap()
	var cmds []tea.Cmd
	m, cmd, _ := m.typeOverSelection()
	cmds = append(cmds, cmd)
	if len(m.extraCursors) > 0 {
		m, cmd = pasteAtAllCursors(m, text)
		cmds = append(cmds, cmd)
	} else if strings.Contains(text, "\n") {
		model, cmd := m.insertPastedText(text)
		m = model.(Model)
		cmds = append(cmds, cmd)
	} else {
		op := document.Op{
			ClientID:   m.clientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: text,
		}
		m.cursor.Col += len([]rune(text))
		m, cmd = applyOp(m, op)
		cmds = append(cmds, cmd)
	}
	m, cmd = m.endTyping()
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// executeDeleteWordBackward is Option+Backspace (and Ctrl+Backspace): delete
// back to the start of the word, or join with the previous line at column 0.
func executeDeleteWordBackward(m Model) (tea.Model, tea.Cmd) {
	if m.sel != nil {
		return executeDeleteSelection(m)
	}
	if m.cursor.Col == 0 {
		return executeInsertBackspace(m)
	}
	runes := []rune(m.buf.Line(m.cursor.Line))
	from, ok := findPrevWordStart(runes, m.cursor.Col)
	if !ok {
		from = 0
	}
	op := document.Op{
		ClientID: m.clientID(),
		Type:     document.OpDelete,
		FromLine: m.cursor.Line, FromCol: from,
		ToLine: m.cursor.Line, ToCol: m.cursor.Col,
	}
	m.cursor.Col = from
	return applyOp(m, op)
}

// executeModelessTab indents the selected lines when there is a selection, and
// otherwise types a tab.
func executeModelessTab(m Model) (tea.Model, tea.Cmd) {
	if m.sel != nil {
		return executeIndent(m)
	}
	return executeInsertTab(m)
}

func executeEnterPalette(m Model) (tea.Model, tea.Cmd) {
	m.mode = ModeCommand
	m.cmdBuf = ""
	m.cmdCompletionIdx = -1
	return m, nil
}

// handleModelessKey runs one key: a typing key joins the open typing run (and
// replaces any selection), anything else ends the run and runs its command.
func (m Model) handleModelessKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	cmd, bound := findIn(insertCmds, []string{key})
	// A [[keymenu]] from the user's config: its key opens the menu.
	if bound && len(cmd.children) > 0 {
		m = m.abandonCompletion()
		m, endCmd := m.endTyping()
		m.prefixSeq = []string{key}
		return m, endCmd
	}
	// Tab over a selection indents it rather than typing over it.
	indenting := bound && cmd.name == "insert-tab" && m.sel != nil
	if bound && cmd.execute != nil && (!typingActions[cmd.name] || indenting) {
		m = m.abandonCompletion()
		m, endCmd := m.endTyping()
		model, runCmd := cmd.execute(m)
		return model, tea.Batch(endCmd, runCmd)
	}
	if !bound && msg.Key().Text == "" {
		return m, nil
	}

	m, beginCmd := m.beginTyping()
	cmds := []tea.Cmd{beginCmd}
	m, delCmd, hadSel := m.typeOverSelection()
	cmds = append(cmds, delCmd)
	if hadSel && (key == "backspace" || key == "delete") {
		return m, tea.Batch(cmds...)
	}
	var model tea.Model
	var runCmd tea.Cmd
	if bound {
		model, runCmd = cmd.execute(m)
	} else {
		model, runCmd = m.insertSelfInsert(msg)
	}
	cmds = append(cmds, runCmd)
	if key == "enter" {
		// A line is a natural undo step: Enter ends the run it belongs to.
		mm, endCmd := model.(Model).endTyping()
		model = mm
		cmds = append(cmds, endCmd)
	}
	return model, tea.Batch(cmds...)
}

// insertCmds is the editor's keymap. Keys are Bubble Tea's names for what the
// terminal sends (see the note at the top of this file). Any key not listed
// here that carries text is typed.
var insertCmds = concatCmds(
	inCategory("Editing", []command{
		{key: "backspace", name: "backspace", label: "Backspace", execute: executeInsertBackspace},
		{key: "delete", name: "delete-forward", label: "Delete forward", execute: executeInsertDelete},
		{key: "enter", name: "newline", label: "Newline", execute: executeInsertEnter},
		{key: "tab", name: "insert-tab", label: "Insert tab", execute: executeModelessTab},
		{key: "shift+tab", name: "unindent", label: "Unindent", execute: executeUnindent},
		{key: "alt+backspace", name: "delete-word-backward", label: "Delete word backward", execute: executeDeleteWordBackward},
		{key: "ctrl+backspace", name: "delete-word-backward", label: "Delete word backward", execute: executeDeleteWordBackward},
		{key: "esc", name: "close-popups", label: "Close popups / clear selection", execute: executeModelessEsc},
	}),
	inCategory("Navigation", []command{
		{key: "left", name: "cursor-left", label: "Cursor left", execute: executeModelessLeft},
		{key: "right", name: "cursor-right", label: "Cursor right", execute: executeModelessRight},
		{key: "up", name: "cursor-up", label: "Cursor up", execute: executeCursorUp},
		{key: "down", name: "cursor-down", label: "Cursor down", execute: executeCursorDown},
		{key: "alt+b", name: "word-left", label: "Word left", execute: executeWordLeft},
		{key: "alt+left", name: "word-left", label: "Word left", execute: executeWordLeft},
		{key: "alt+f", name: "word-right", label: "Word right", execute: executeWordRight},
		{key: "alt+right", name: "word-right", label: "Word right", execute: executeWordRight},
		{key: "home", name: "go-to-line-start", label: "Go to line start", execute: executeGoToLineStart},
		{key: "ctrl+a", name: "go-to-line-start", label: "Go to line start", execute: executeGoToLineStart},
		{key: "end", name: "go-to-line-end", label: "Go to line end", execute: executeGoToLineEnd},
		{key: "ctrl+e", name: "go-to-line-end", label: "Go to line end", execute: executeGoToLineEnd},
		{key: "pgup", name: "page-up", label: "Page up", execute: executePageUp},
		{key: "pgdown", name: "page-down", label: "Page down", execute: executePageDown},
		{key: "ctrl+home", name: "go-to-top", label: "Go to top of file", execute: executeGoToTop},
		{key: "ctrl+end", name: "go-to-end", label: "Go to end of file", execute: executeGoToEnd},
		{key: "ctrl+-", name: "jump-back", label: "Jump back", execute: executeJumpBack},
		{key: "ctrl+=", name: "jump-forward", label: "Jump forward", execute: executeJumpForward},
	}),
	inCategory("Selection", []command{
		{key: "shift+left", name: "extend-char-backward", label: "Extend selection left", execute: executeExtendCharBackward},
		{key: "shift+right", name: "extend-char-forward", label: "Extend selection right", execute: executeExtendCharForward},
		{key: "shift+up", name: "extend-line-up", label: "Extend selection up", execute: executeExtendLineUp},
		{key: "shift+down", name: "extend-line-down", label: "Extend selection down", execute: executeExtendLineDown},
		{key: "alt+shift+left", name: "extend-word-backward", label: "Extend word backward", execute: executeExtendWordBackward},
		{key: "alt+shift+right", name: "extend-word-forward", label: "Extend word forward", execute: executeExtendWordForward},
		{key: "shift+home", name: "extend-line-start", label: "Select to line start", execute: executeExtendLineStart},
		{key: "shift+meta+left", name: "extend-line-start", label: "Select to line start", execute: executeExtendLineStart},
		{key: "shift+end", name: "extend-line-end", label: "Select to line end", execute: executeExtendLineEnd},
		{key: "shift+meta+right", name: "extend-line-end", label: "Select to line end", execute: executeExtendLineEnd},
		{key: "ctrl+shift+a", name: "select-all", label: "Select all", execute: executeSelectAll},
		{key: "ctrl+d", name: "select-next-occurrence", label: "Select next occurrence", execute: executeSelectNextOccurrence},
	}),
	inCategory("Editing", []command{
		{key: "ctrl+z", name: "undo", label: "Undo", execute: executeUndo},
		{key: "ctrl+shift+z", name: "redo", label: "Redo", execute: executeRedo},
		{key: "ctrl+c", name: "copy", label: "Copy", execute: executeModelessCopy},
		{key: "ctrl+x", name: "cut", label: "Cut", execute: executeModelessCut},
		{key: "ctrl+v", name: "paste", label: "Paste", execute: executeModelessPaste},
		{key: "ctrl+/", name: "toggle-comment", label: "Toggle comment", execute: executeToggleComment},
		{key: "ctrl+_", name: "toggle-comment", label: "Toggle comment", execute: executeToggleComment},
		{key: "ctrl+j", name: "join-lines", label: "Join lines", execute: executeJoinLines},
		{key: "ctrl+shift+up", name: "move-line-up", label: "Move line(s) up", execute: executeMoveLineUp},
		{key: "ctrl+shift+down", name: "move-line-down", label: "Move line(s) down", execute: executeMoveLineDown},
		{key: "ctrl+i", name: "trigger-completion", label: "Trigger completion", execute: executeTriggerCompletion},
	}),
	inCategory("Search", []command{
		{key: "ctrl+f", name: "search", label: "Search", execute: executeEnterSearchMode},
		{key: "f3", name: "search-next", label: "Search next", execute: normalAction("search-next")},
		{key: "shift+f3", name: "search-previous", label: "Search previous", execute: normalAction("search-previous")},
	}),
	inCategory("Files & Buffers", []command{
		{key: "ctrl+s", name: "save", label: "Save", execute: executeSave},
		{key: "ctrl+shift+s", name: "save-as", label: "Save As", execute: sendMsg(saveAsPromptMsg{})},
		{key: "ctrl+n", name: "new-file", label: "New file", execute: sendMsg(OpenNewFileMsg{})},
		{key: "ctrl+p", name: "open-file-picker", label: "Open file picker", execute: executeOpenFilePicker},
		{key: "ctrl+w", name: "close-buffer", label: "Close buffer", execute: executeModelessClose},
		{key: "ctrl+q", name: "quit-editor", label: "Quit indigo", execute: sendMsg(QuitRequestMsg{})},
		{key: "ctrl+shift+left", name: "prev-buffer", label: "Previous buffer", execute: executePrevBuffer},
		{key: "ctrl+shift+right", name: "next-buffer", label: "Next buffer", execute: executeNextBuffer},
	}),
	inCategory("Commands", []command{
		{key: "ctrl+space", name: "command-menu", label: "Command menu", execute: executeOpenSpaceMenu},
		{key: "ctrl+shift+p", name: "command-palette", label: "Command palette", execute: executeEnterPalette},
		// Ctrl+? is Ctrl+Shift+/: a terminal reporting keys the kitty way names
		// the unshifted key, others the character.
		{key: "ctrl+shift+/", name: "show-plugin-bindings", label: "Show key bindings", execute: executeShowHelp},
		{key: "ctrl+?", name: "show-plugin-bindings", label: "Show key bindings", execute: executeShowHelp},
		{key: "f1", name: "show-plugin-bindings", label: "Show key bindings", execute: executeShowHelp},
	}),
	inCategory("LSP / Diagnostics", []command{
		{key: "ctrl+k", name: "hover-docs", label: "Hover docs", execute: executeHover},
		{key: "f12", name: "go-to-definition", label: "Go to definition", execute: executeGoToDefinition},
		{key: "shift+f12", name: "find-references", label: "Find references", execute: normalAction("find-references")},
	}),
	inCategory("Debug", []command{
		{key: "f5", name: "debug-continue-or-start", label: "Continue / start debugging", execute: executeDebugContinueOrStart},
		{key: "shift+f5", name: "debug-stop", label: "Stop debugging", execute: executeDebugStop},
		{key: "f9", name: "debug-toggle-breakpoint", label: "Toggle breakpoint", execute: executeToggleBreakpoint},
		{key: "f10", name: "debug-step-over", label: "Step over", execute: executeDebugNext},
		{key: "f11", name: "debug-step-in", label: "Step in", execute: executeDebugStepIn},
		{key: "shift+f11", name: "debug-step-out", label: "Step out", execute: executeDebugStepOut},
	}),
)

// inCategory sets the help popup's category on each command.
func inCategory(category string, cmds []command) []command {
	for i := range cmds {
		cmds[i].category = category
	}
	return cmds
}

func concatCmds(groups ...[]command) []command {
	var out []command
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
