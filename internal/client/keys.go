package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

// command is a node in the prefix-command tree.
// Leaf nodes have execute set; branch nodes have children.
type command struct {
	key string
	// name is the stable, config-facing identifier for this leaf's action
	// (see config.Keybind.Action). Branch nodes (menus) leave it empty.
	name      string
	label     string
	menuTitle string
	// category is the generated help popup's display grouping (see
	// help_gen.go). Set explicitly on root-level leaves and on a menu's own
	// node (in which case every descendant inherits it, so a menu's leaves
	// don't each need their own); left empty on a leaf to inherit from its
	// nearest ancestor.
	category string
	children []command
	execute  func(m Model) (tea.Model, tea.Cmd)
}

// executeOpenFilePicker, executePrevBuffer, executeNextBuffer, and
// executeOpenSymbolPicker are extracted (rather than left as inline
// closures) because each is bound at more than one point in the tree
// (a root key and a menu leaf) — giving both sites the same name only
// means the same action once both actually point at the same function
// value; see TestNoDuplicateActionNames.
func executeOpenFilePicker(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return OpenPickerMsg{} }
}

func executePrevBuffer(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return PrevBufferMsg{} }
}

func executeNextBuffer(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return NextBufferMsg{} }
}

func executeOpenSymbolPicker(m Model) (tea.Model, tea.Cmd) {
	return m, func() tea.Msg { return OpenSymbolPickerMsg{BufID: m.bufID} }
}

// prefixCmds is the root of the prefix-command tree for Normal mode.
var prefixCmds = []command{
	{key: "ctrl+p", name: "open-file-picker", category: "Files & Buffers", label: "Open file picker", execute: executeOpenFilePicker},
	{key: "n", name: "search-next", category: "Search", label: "Search next", execute: func(m Model) (tea.Model, tea.Cmd) {
		if len(m.searchMatches) > 0 {
			m.searchIdx = (m.searchIdx + 1) % len(m.searchMatches)
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}
		return m, nil
	}},
	{key: "N", name: "search-previous", category: "Search", label: "Search previous", execute: func(m Model) (tea.Model, tea.Cmd) {
		if len(m.searchMatches) > 0 {
			m.searchIdx = (m.searchIdx - 1 + len(m.searchMatches)) % len(m.searchMatches)
			sm := m.searchMatches[m.searchIdx]
			m.cursor = document.Pos{Line: sm.line, Col: sm.col}
			m.scrollToCursor()
		}
		return m, nil
	}},
	{key: "i", name: "insert-before-cursor", category: "Editing", label: "Insert before cursor", execute: func(m Model) (tea.Model, tea.Cmd) {
		m = m.withClearedSearch()
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.groupBefore = m.cursorSnap()
		m.insertLineCount = m.buf.LineCount()
		m.mode = ModeInsert
		return m, nil
	}},
	{key: "ctrl+c", name: "quit-hint", category: "System", label: "Quit hint", execute: executeCancelHint},
	{key: "ctrl+h", name: "prev-buffer", category: "Files & Buffers", label: "Previous buffer", execute: executePrevBuffer},
	{key: "ctrl+l", name: "next-buffer", category: "Files & Buffers", label: "Next buffer", execute: executeNextBuffer},
	{key: ":", name: "command-mode", category: "System", label: "Command mode", execute: executeEnterCommandMode},
	{key: "/", name: "search", category: "Search", label: "Search", execute: executeEnterSearchMode},
	{key: "esc", name: "cancel-selection", category: "Selection", label: "Cancel selection", execute: executeEscNormal},
	{key: "a", name: "append-after-cursor", category: "Editing", label: "Append after cursor", execute: executeAppendAfterCursor},
	{key: "A", name: "append-line-end", category: "Editing", label: "Append at line end", execute: executeAppendLineEnd},
	{key: "o", name: "open-line-below", category: "Editing", label: "Open line below", execute: executeOpenLineBelow},
	{key: "O", name: "open-line-above", category: "Editing", label: "Open line above", execute: executeOpenLineAbove},
	{key: "ctrl+s", name: "save", category: "Files & Buffers", label: "Save", execute: executeSave},
	{key: "K", name: "hover-docs", category: "LSP / Diagnostics", label: "Hover docs", execute: executeHover},
	{key: "J", name: "join-lines", category: "Editing", label: "Join lines", execute: executeJoinLines},
	{key: "D", name: "toggle-diagnostics-popup", category: "LSP / Diagnostics", label: "Toggle diagnostics popup", execute: executeToggleDiagPopup},
	{key: "u", name: "undo", category: "Editing", label: "Undo", execute: executeUndo},
	{key: "U", name: "redo", category: "Editing", label: "Redo", execute: executeRedo},
	{key: "W", name: "extend-next-word-start", category: "Selection", label: "Extend to next word start", execute: executeExtendNextWordStart},
	{key: "E", name: "extend-word-forward", category: "Selection", label: "Extend word forward", execute: executeExtendWordForward},
	{key: "x", name: "select-line", category: "Selection", label: "Select line", execute: executeSelectLine},
	{key: "X", name: "extend-line-backward", category: "Selection", label: "Extend line backward", execute: executeExtendLineBackward},
	{key: "%", name: "select-all", category: "Selection", label: "Select all", execute: executeSelectAll},
	{key: ";", name: "clear-selections", category: "Selection", label: "Clear selections", execute: executeClearSelections},
	{key: "alt+;", name: "flip-selection", category: "Selection", label: "Flip selection", execute: executeFlipSelection},
	{key: "d", name: "delete-selection", category: "Editing", label: "Delete selection", execute: executeDeleteSelection},
	{key: "c", name: "change-selection", category: "Editing", label: "Change selection", execute: executeChangeSelection},
	{key: "y", name: "yank", category: "Editing", label: "Yank", execute: executeYank},
	{key: "v", name: "cut-selection", category: "Editing", label: "Cut selection", execute: executeCutSelection},
	// hjkl are deliberately not bound by default (design decision: freed up
	// as bare-key slots rather than kept as duplicate arrow-key aliases —
	// see PLAN.md's keybinding-tiering doc). Still available via
	// [[keybind]] for anyone who wants them back.
	{key: "left", name: "cursor-left", category: "Navigation", label: "Cursor left", execute: executeCursorLeft},
	{key: "right", name: "cursor-right", category: "Navigation", label: "Cursor right", execute: executeCursorRight},
	{key: "down", name: "cursor-down", category: "Navigation", label: "Cursor down", execute: executeCursorDown},
	{key: "up", name: "cursor-up", category: "Navigation", label: "Cursor up", execute: executeCursorUp},
	{key: "ctrl+f", name: "page-down", category: "Navigation", label: "Page down", execute: executePageDown},
	{key: "pgdown", name: "page-down", category: "Navigation", label: "Page down", execute: executePageDown},
	{key: "ctrl+b", name: "page-up", category: "Navigation", label: "Page up", execute: executePageUp},
	{key: "pgup", name: "page-up", category: "Navigation", label: "Page up", execute: executePageUp},
	{key: "G", name: "go-to-last-line", category: "Navigation", label: "Go to last line", execute: executeGoToLastLine},
	{key: "0", name: "go-to-line-start", category: "Navigation", label: "Go to line start", execute: executeGoToLineStart},
	{key: "home", name: "go-to-line-start", category: "Navigation", label: "Go to line start", execute: executeGoToLineStart},
	{key: "^", name: "go-to-first-non-blank", category: "Navigation", label: "Go to first non-blank", execute: executeFirstNonBlank},
	{key: "$", name: "go-to-line-end", category: "Navigation", label: "Go to line end", execute: executeGoToLineEnd},
	{key: "end", name: "go-to-line-end", category: "Navigation", label: "Go to line end", execute: executeGoToLineEnd},
	{key: "shift+home", name: "extend-line-start", category: "Selection", label: "Select to line start", execute: executeExtendLineStart},
	{key: "shift+end", name: "extend-line-end", category: "Selection", label: "Select to line end", execute: executeExtendLineEnd},
	{key: "shift+right", name: "extend-char-forward", category: "Selection", label: "Extend selection right", execute: executeExtendCharForward},
	{key: "shift+left", name: "extend-char-backward", category: "Selection", label: "Extend selection left", execute: executeExtendCharBackward},
	{key: "w", name: "next-word-start", category: "Navigation", label: "Next word start", execute: executeNextWordStart},
	{key: "b", name: "previous-word-start", category: "Navigation", label: "Previous word start", execute: executePrevWordStart},
	{key: "e", name: "word-end", category: "Navigation", label: "Word end", execute: executeWordEnd},
	{key: "p", name: "paste", category: "Editing", label: "Paste", execute: executePaste},
	{key: "B", name: "extend-word-backward", category: "Selection", label: "Extend word backward", execute: executeExtendWordBackward},
	{key: "ctrl+d", name: "select-next-occurrence", category: "Multi-cursor", label: "Select next occurrence", execute: executeSelectNextOccurrence},
	{key: "C", name: "add-cursor-below", category: "Multi-cursor", label: "Add cursor below", execute: executeAddCursorBelow},
	// ctrl+/ and ctrl+_ are the same byte (0x1F) in most terminals.
	{key: "ctrl+/", name: "toggle-comment", category: "Editing", label: "Toggle comment", execute: executeToggleComment},
	{key: "ctrl+_", name: "toggle-comment", category: "Editing", label: "Toggle comment", execute: executeToggleComment},
	{key: "?", name: "show-plugin-bindings", category: "System", label: "Show key bindings", execute: executeFetchPluginBindings},
	{key: "alt+s", name: "split-selection-into-cursors", category: "Multi-cursor", label: "Split selection into cursors", execute: executeSplitSelectionIntoCursors},
	{key: "z", name: "set-mark", category: "Marks & Macros", label: "Set mark", execute: executeSetMark},
	{key: "Z", name: "select-to-mark", category: "Marks & Macros", label: "Select to mark", execute: executeSelectToMark},
	{key: "q", name: "macro-record-toggle", category: "Marks & Macros", label: "Start/stop recording macro", execute: executeMacroRecordToggle},
	{key: "@", name: "macro-replay", category: "Marks & Macros", label: "Replay macro", execute: executeMacroReplay},
	{key: ">", name: "indent", category: "Editing", label: "Indent", execute: executeIndent},
	{key: "<", name: "unindent", category: "Editing", label: "Unindent", execute: executeUnindent},
	// Move current line (or selected lines) up/down, swapping with the
	// neighbor. Bound to Shift+Arrow rather than Alt+Arrow: zellij's default
	// pane-focus keymap claims Alt+Arrow even in "locked" interface mode.
	{key: "shift+up", name: "move-line-up", category: "Editing", label: "Move line(s) up", execute: executeMoveLineUp},
	{key: "shift+down", name: "move-line-down", category: "Editing", label: "Move line(s) down", execute: executeMoveLineDown},
	{key: "-", name: "jump-back", category: "Navigation", label: "Jump back", execute: executeJumpBack},
	{key: "=", name: "jump-forward", category: "Navigation", label: "Jump forward", execute: executeJumpForward},
	{key: "+", name: "jump-forward", category: "Navigation", label: "Jump forward", execute: executeJumpForward},
	{
		key:       "g",
		label:     "Go",
		menuTitle: "Go",
		category:  "Go to (g)",
		children: []command{
			{key: "g", name: "go-to-top", label: "Go to top of file", execute: executeGoToTop},
			{key: "e", name: "go-to-end", label: "Go to end of file", execute: executeGoToEnd},
			{key: "d", name: "go-to-definition", label: "Go to definition", execute: executeGoToDefinition},
			{key: "h", name: "go-to-line-start", label: "Go to line start", execute: executeGoToLineStart},
			{key: "l", name: "go-to-line-end", label: "Go to line end", execute: executeGoToLineEnd},
			{key: "s", name: "go-to-symbol-in-project", label: "Go to symbol in project", execute: executeOpenSymbolPicker},
			{key: "S", name: "go-to-symbol-in-file", label: "Go to symbol in file", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, m.fetchDocSymbols()
			}},
			{key: "r", name: "find-references", label: "Find references", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, m.fetchReferences()
			}},
			{key: "b", name: "open-buffer-picker", label: "Open buffer picker", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return OpenBufPickerMsg{} }
			}},
		},
	},
	{
		key:       "]",
		label:     "Next",
		menuTitle: "Next",
		category:  "Files & Buffers",
		children: []command{
			{key: "b", name: "next-buffer", label: "Next buffer", execute: executeNextBuffer},
		},
	},
	{
		key:       "[",
		label:     "Prev",
		menuTitle: "Prev",
		category:  "Files & Buffers",
		children: []command{
			{key: "b", name: "prev-buffer", label: "Previous buffer", execute: executePrevBuffer},
		},
	},
	commandMenuRoot,
	{
		key:       "~",
		label:     "Case",
		menuTitle: "Case",
		category:  "Case (~)",
		children: []command{
			{key: "s", name: "case-to-snake", label: "snake_case", execute: executeCaseConvertSnake},
			{key: "S", name: "case-to-screaming-snake", label: "SCREAMING_SNAKE_CASE", execute: executeCaseConvertScreamingSnake},
			{key: "c", name: "case-to-camel", label: "camelCase", execute: executeCaseConvertCamel},
			{key: "p", name: "case-to-pascal", label: "PascalCase", execute: executeCaseConvertPascal},
			{key: "k", name: "case-to-kebab", label: "kebab-case", execute: executeCaseConvertKebab},
			{key: "d", name: "case-to-dot", label: "dot.case", execute: executeCaseConvertDot},
		},
	},
	{
		key:       "M",
		label:     "Move",
		menuTitle: "Move",
		category:  "Editing",
		children: []command{
			{key: "j", name: "move-line-down", label: "Move line(s) down", execute: executeMoveLineDown},
			{key: "k", name: "move-line-up", label: "Move line(s) up", execute: executeMoveLineUp},
		},
	},
	{
		key:       "s",
		label:     "Sort",
		menuTitle: "Sort",
		category:  "Sort (s)",
		children: []command{
			{key: "a", name: "sort-lines-ascending", label: "Sort lines ascending", execute: executeSortLinesAscending},
			{key: "d", name: "sort-lines-descending", label: "Sort lines descending", execute: executeSortLinesDescending},
		},
	},
	{
		key:       "m",
		label:     "Match",
		menuTitle: "Match",
		category:  "Match (m)",
		children: []command{
			{key: "m", name: "go-to-matching-bracket", label: "Go to matching bracket", execute: executeGotoMatchingBracket},
			{
				key:       "i",
				label:     "Select inside object",
				menuTitle: "Match Inside",
				children: []command{
					{key: "w", name: "select-inside-word", label: "Word", execute: executeSelectInsideWord},
					{key: "s", name: "select-inside-whitespace", label: "Whitespace", execute: executeSelectInsideWhitespace},
					{key: "m", name: "select-inside-brackets", label: "Closest surrounding pair", execute: executeSelectInsideBrackets},
					{key: ".", name: "select-inside-delimiter", label: "Quote/delimiter pair", execute: executeSelectInsideChar},
					{key: "f", name: "select-inside-function", label: "Function", execute: executeSelectInsideFunction},
					{key: "t", name: "select-inside-type", label: "Type definition", execute: executeSelectInsideType},
					{key: "a", name: "select-inside-argument", label: "Argument/parameter", execute: executeSelectInsideArgument},
					{key: "c", name: "select-inside-comment", label: "Comment", execute: executeSelectInsideComment},
				},
			},
			{
				key:       "a",
				label:     "Select around object",
				menuTitle: "Match Around",
				children: []command{
					{key: "w", name: "select-around-word", label: "Word", execute: executeSelectInsideWord},
					{key: "s", name: "select-around-whitespace", label: "Whitespace", execute: executeSelectInsideWhitespace},
					{key: "m", name: "select-around-brackets", label: "Closest surrounding pair", execute: executeSelectAroundBrackets},
					{key: ".", name: "select-around-delimiter", label: "Quote/delimiter pair", execute: executeSelectAroundChar},
					{key: "f", name: "select-around-function", label: "Function", execute: executeSelectAroundFunction},
					{key: "t", name: "select-around-type", label: "Type definition", execute: executeSelectAroundType},
					{key: "a", name: "select-around-argument", label: "Argument/parameter", execute: executeSelectAroundArgument},
					{key: "c", name: "select-around-comment", label: "Comment", execute: executeSelectAroundComment},
				},
			},
		},
	},
}

// commandMenuRoot is the top-level node for the Command (space) menu. Its
// children are the editor's built-in entries; plugin-contributed entries
// (declared via plugin.toml menu_item and invoked through OnMenuAction) are
// merged in at lookup time by resolveCommand, not listed here.
var commandMenuRoot = command{
	key:       " ",
	label:     "Command",
	menuTitle: "Command",
	category:  "Command (space)",
	children: []command{
		{key: "s", name: "search-and-replace", label: "Search & Replace", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenSearchReplaceMsg{} }
		}},
		{key: "S", name: "save-as", label: "Save As", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return saveAsPromptMsg{} }
		}},
		// name/label reused verbatim from g's "s" child — same action, same
		// canonical label, required by TestNoDuplicateActionLabels.
		{key: "p", name: "go-to-symbol-in-project", label: "Go to symbol in project", execute: executeOpenSymbolPicker},
		// name/label reused verbatim from root ctrl+p — same action, same
		// canonical label, required by TestNoDuplicateActionLabels.
		{key: "f", name: "open-file-picker", label: "Open file picker", execute: executeOpenFilePicker},
		{key: "l", name: "show-message-log", label: "Message Log", execute: func(m Model) (tea.Model, tea.Cmd) {
			m.msgLogVisible = true
			m.msgLogScroll = messageLogMaxScroll(m.width, m.height, m.messageLog) // start on the last page (most recent)
			return m, nil
		}},
		{key: "n", name: "new-file", label: "New file", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenNewFileMsg{} }
		}},
		{key: "a", name: "code-actions", label: "Code Actions (fixes & refactors)", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Uses the current selection as the request range when one is
			// active, so range refactors (Extract Function/Variable) are
			// offered alongside point-based quick-fixes.
			return m, m.fetchFixes()
		}},
		{key: "i", name: "organize-imports", label: "Organize Imports", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Applies the language server's source.organizeImports action
			// directly (no picker) unlike "a" Code Actions — see
			// doOrganizeImports / EditorService.lspOrganizeImports.
			return m, m.doOrganizeImports()
		}},
		{key: "r", name: "start-rename-symbol", label: "Refactor: Rename Symbol", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Uses the language server if one is running for this buffer;
			// see doRenameSymbol / EditorService.lspRename.
			m.mode = ModeCommand
			m.cmdBuf = "rename "
			m.cmdCompletionIdx = -1
			return m, nil
		}},
		{key: "m", name: "start-move-to-file", label: "Refactor: Move Function to File", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Tree-sitter based (no language server needed); see
			// doMoveFunctionToFile / EditorService.moveTextToFile.
			m.mode = ModeCommand
			m.cmdBuf = "move-to-file "
			m.cmdCompletionIdx = -1
			return m, nil
		}},
	},
}

// findCommand navigates prefixCmds following seq and returns the final node.
func findCommand(seq []string) (*command, bool) {
	return findIn(prefixCmds, seq)
}

// findIn walks cmds following seq and returns the final node, recursing into
// each matched node's children for the next rune in seq.
func findIn(cmds []command, seq []string) (*command, bool) {
	var found *command
	for _, r := range seq {
		matched := false
		for i := range cmds {
			if cmds[i].key == r {
				found = &cmds[i]
				cmds = cmds[i].children
				matched = true
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return found, found != nil
}

// resolveCommand is like findCommand but, when seq starts at the Command
// (space) menu, merges plugin-contributed menu items into its children before
// resolving the rest of seq against it. This lets plugins add entries (and
// submenus) without the core prefixCmds tree knowing about them ahead of time.
func (m Model) resolveCommand(seq []string) (*command, bool) {
	if len(seq) == 0 || seq[0] != " " {
		return findCommand(seq)
	}
	node := commandMenuRoot
	node.children = append(append([]command{}, node.children...), m.pluginMenuCommands()...)
	if len(seq) == 1 {
		return &node, true
	}
	return findIn(node.children, seq[1:])
}

// pluginMenuCommands converts the cached plugin-contributed menu tree into
// []command, recursively. Leaf items dispatch through handleMenuActionRPC;
// group items (Command == "" with children) just descend further.
func (m Model) pluginMenuCommands() []command {
	if m.rpc == nil {
		return nil
	}
	return clientMenuItemsToCommands(m.rpc.MenuItems())
}

func clientMenuItemsToCommands(items []ClientMenuItem) []command {
	if len(items) == 0 {
		return nil
	}
	out := make([]command, len(items))
	for i, it := range items {
		r := keyRune(it.Key, it.Label)
		c := command{key: r, label: it.Label}
		if len(it.Children) > 0 {
			c.menuTitle = it.Label
			c.children = clientMenuItemsToCommands(it.Children)
		} else {
			pluginName, action := it.PluginName, it.Command
			c.execute = func(m Model) (tea.Model, tea.Cmd) {
				return m.handleMenuActionRPC(pluginName, action)
			}
		}
		out[i] = c
	}
	return out
}

// keyRune picks the popup-selector key for a plugin menu item: the declared
// Key if set, otherwise the lowercased first rune of Label.
func keyRune(key, label string) string {
	if key != "" {
		return key
	}
	for _, r := range strings.ToLower(label) {
		return string(r)
	}
	return ""
}

// handleMenuActionRPC dispatches a Command-menu selection to the plugin that
// registered it, mirroring handlePluginKeyRPC's result handling.
func (m Model) handleMenuActionRPC(pluginName, action string) (tea.Model, tea.Cmd) {
	bufID := m.bufID
	curLine := uint32(m.cursor.Line)
	curCol := uint32(m.cursor.Col)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.InvokeMenuAction(ctx, pluginName, action, bufID, curLine, curCol)
		if err != nil {
			return errorMsg{err}
		}
		return pluginKeyResultMsg{bufID: bufID, result: result}
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The severe-error modal (see pushSevereError) blocks all other input
	// until explicitly dismissed — unlike the toast overlay, which is purely
	// visual, this covers failures the user must consciously acknowledge
	// (an edit that may not have reached the server, or a resync whose
	// outcome they need to check) rather than risk them typing straight
	// through it unread.
	if m.severeErr != "" {
		switch msg.String() {
		case "enter", "esc":
			m.severeErr = ""
		}
		return m, nil
	}
	if m.recoveryPrompt {
		return m.handleRecoveryPrompt(msg)
	}
	// Escape dismisses the diagnostic popup and suppresses re-show until cursor leaves the range.
	// Always falls through so the mode transition (insert→normal) also happens.
	if m.diagPopup && msg.String() == "esc" {
		m.diagPopup = false
		m.diagPopupSuppressed = true
	}

	// Scroll keys navigate the help popup; q/esc/? dismiss it.
	if m.helpVisible {
		helpLines := helpPopupLines(m.pluginBindings, helpPopupInnerWidth(m.width))
		maxPopH := max(6, m.height-5) // matches vis-4 in View() where vis = m.height-1
		contentH := maxPopH - 2
		maxScroll := max(0, len(helpLines)-contentH)
		switch msg.String() {
		case "j", "down":
			m.helpScroll = min(m.helpScroll+1, maxScroll)
			return m, nil
		case "k", "up":
			m.helpScroll = max(0, m.helpScroll-1)
			return m, nil
		case "ctrl+f":
			m.helpScroll = min(m.helpScroll+m.height/4, maxScroll)
			return m, nil
		case "ctrl+b":
			m.helpScroll = max(0, m.helpScroll-m.height/4)
			return m, nil
		case "esc", "q", "?":
			m.helpVisible = false
			m.helpScroll = 0
			return m, nil
		default:
			m.helpVisible = false
			m.helpScroll = 0
			// Don't consume: let the key fall through to normal handling.
		}
	}

	// Scroll keys navigate the message-log popup; q/esc dismiss it.
	if m.msgLogVisible {
		maxScroll := messageLogMaxScroll(m.width, m.height, m.messageLog)
		switch msg.String() {
		case "j", "down":
			m.msgLogScroll = min(m.msgLogScroll+1, maxScroll)
			return m, nil
		case "k", "up":
			m.msgLogScroll = max(0, m.msgLogScroll-1)
			return m, nil
		case "ctrl+f":
			m.msgLogScroll = min(m.msgLogScroll+m.height/4, maxScroll)
			return m, nil
		case "ctrl+b":
			m.msgLogScroll = max(0, m.msgLogScroll-m.height/4)
			return m, nil
		case "esc", "q":
			m.msgLogVisible = false
			m.msgLogScroll = 0
			return m, nil
		default:
			m.msgLogVisible = false
			m.msgLogScroll = 0
			// Don't consume: let the key fall through to normal handling.
		}
	}

	// Scroll keys navigate the hover popup; all other keys dismiss it.
	if m.hoverContent != nil {
		// contentH mirrors the calculation in renderHoverPopup.
		maxPopH := max(6, m.height-4)
		contentH := maxPopH - 2
		maxScroll := max(0, m.hoverTotalLines-contentH)
		switch msg.String() {
		case "j", "down":
			m.hoverScroll = min(m.hoverScroll+1, maxScroll)
			return m, nil
		case "k", "up":
			m.hoverScroll = max(0, m.hoverScroll-1)
			return m, nil
		case "ctrl+f":
			m.hoverScroll = min(m.hoverScroll+m.height/4, maxScroll)
			return m, nil
		case "ctrl+b":
			m.hoverScroll = max(0, m.hoverScroll-m.height/4)
			return m, nil
		case "esc":
			m.hoverContent = nil
			m.hoverScroll = 0
			return m, nil
		default:
			m.hoverContent = nil
			m.hoverScroll = 0
			// Don't consume: let the key fall through to normal handling.
		}
	}
	if m.saveAsInput != nil {
		return m.handleSaveAsDialog(msg)
	}
	// Clear transient error on any key.
	m = m.pushStatus("")
	switch m.mode {
	case ModeNormal:
		return m.handleNormal(msg)
	case ModeInsert:
		return m.handleInsert(msg)
	case ModeCommand:
		return m.handleCommand(msg)
	case ModeSearch:
		return m.handleSearch(msg)
	}
	return m, nil
}

func (m Model) handleSaveAsDialog(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.saveAsInput = nil
		m.saveAsThenClose = false
		return m, nil
	case "enter":
		input := strings.TrimSpace(*m.saveAsInput)
		m.saveAsInput = nil
		if input == "" {
			return m, nil
		}
		newPath, err := filepath.Abs(input)
		if err != nil {
			m = m.pushStatus(fmt.Sprintf("E: bad path: %v", err))
			m.saveAsThenClose = false
			return m, nil
		}
		return m, m.doSaveAsNow(newPath, m.saveAsThenClose)
	case "backspace", "ctrl+h":
		runes := []rune(*m.saveAsInput)
		if len(runes) > 0 {
			s := string(runes[:len(runes)-1])
			m.saveAsInput = &s
		}
		return m, nil
	default:
		// Append printable characters.
		if msg.Type == tea.KeyRunes {
			s := *m.saveAsInput + string(msg.Runes)
			m.saveAsInput = &s
		}
		return m, nil
	}
}

func (m Model) handleRecoveryPrompt(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		m.recoveryPrompt = false
	case "n", "N", "esc":
		m.recoveryPrompt = false
		// Captured synchronously (m.buf is a shared *document.Buffer) — see
		// doSaveNow's comment in ops.go for why this can't be read inside
		// the closure below.
		startVersion := m.buf.Version()
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			content, err := m.rpc.DiscardRecovery(ctx, m.bufID)
			if err != nil {
				return discardRecoveryFailedMsg{bufID: m.bufID, err: err}
			}
			return discardRecoveryMsg{bufID: m.bufID, version: startVersion, content: content}
		}
	}
	return m, nil
}

// handleCapturedKey forwards a keypress to the plugin that requested capture mode.
// Esc always exits capture mode locally; it is still forwarded to the plugin so
// the plugin can clean up its own state. Each non-Esc key decrements the remaining
// count; when it hits zero, capture mode ends (unless the plugin's response re-enables it).
func (m Model) handleCapturedKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	isEsc := key == "esc"

	if isEsc {
		m.captureMode = false
		m.captureRemaining = 0
	} else {
		if m.captureRemaining > 0 {
			m.captureRemaining--
		}
		if m.captureRemaining == 0 {
			m.captureMode = false
		}
	}

	bufID := m.bufID
	curLine := uint32(m.cursor.Line)
	curCol := uint32(m.cursor.Col)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "capture", bufID, curLine, curCol)
		if err != nil {
			if isEsc {
				return nil // suppress error on esc-cancel
			}
			return errorMsg{err}
		}
		return pluginKeyResultMsg{bufID: bufID, result: result}
	}
}

// handlePluginKeyRPC dispatches a keypress to the owning plugin and returns a
// cmd that will deliver the result as a pluginKeyResultMsg.
func (m Model) handlePluginKeyRPC(key string) (tea.Model, tea.Cmd) {
	bufID := m.bufID
	curLine := uint32(m.cursor.Line)
	curCol := uint32(m.cursor.Col)
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := m.rpc.HandlePluginKey(ctx, key, "normal", bufID, curLine, curCol)
		if err != nil {
			return errorMsg{err}
		}
		return pluginKeyResultMsg{bufID: bufID, result: result}
	}
}

// handleFixPopup handles keyboard input while the fix popup is visible.
func (m Model) handleFixPopup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.fixItems = nil
		m.fixDecor = nil
		return m, nil
	case "j", "down":
		if m.fixIdx < len(m.fixItems)-1 {
			m.fixIdx++
		}
		return m, nil
	case "k", "up":
		if m.fixIdx > 0 {
			m.fixIdx--
		}
		return m, nil
	case "enter":
		idx := m.fixIdx
		// LSP-edit fixes apply synchronously through the undo-aware batch
		// path (they must mutate the model's undo stack and buffer);
		// plugin fixes/replacements go through the async command as before.
		if idx >= 0 && idx < len(m.fixItems) && len(m.fixItems[idx].LspEdits) > 0 {
			item := m.fixItems[idx]
			m.fixItems = nil
			m.fixDecor = nil
			// Range-extract refactors (Extract Function/Extract Variable)
			// introduce a brand-new symbol the server names itself
			// ("newFunction"/"newVar") — prompt for the real name instead
			// of applying that default.
			if strings.HasPrefix(item.LspKind, "refactor.extract.") {
				return startExtractRenamePrompt(m, item.LspEdits, item.LspKind)
			}
			return applyLspEdits(m, item.LspEdits)
		}
		cmd := m.applyFixCmd(idx)
		m.fixItems = nil
		m.fixDecor = nil
		return m, cmd
	}
	return m, nil
}
