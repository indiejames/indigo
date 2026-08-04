package client

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// command is a node in the prefix-command tree.
// Leaf nodes have execute set; branch nodes have children.
type command struct {
	key       rune
	label     string
	menuTitle string
	children  []command
	execute   func(m Model) (tea.Model, tea.Cmd)
}

// prefixCmds is the root of the prefix-command tree for Normal mode.
var prefixCmds = []command{
	{
		key:       'g',
		label:     "Go",
		menuTitle: "Go",
		children: []command{
			{key: 'g', label: "Go to top of file", execute: executeGoToTop},
			{key: 'e', label: "Go to end of file", execute: executeGoToEnd},
			{key: 'd', label: "Go to definition", execute: executeGoToDefinition},
			{key: 'h', label: "Go to line start", execute: executeGoToLineStart},
			{key: 'l', label: "Go to line end", execute: executeGoToLineEnd},
			{key: 's', label: "Go to symbol in project", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return OpenSymbolPickerMsg{BufID: m.bufID} }
			}},
			{key: 'S', label: "Go to symbol in file", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, m.fetchDocSymbols()
			}},
			{key: 'r', label: "Find references", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, m.fetchReferences()
			}},
			{key: 'b', label: "Open buffer picker", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return OpenBufPickerMsg{} }
			}},
		},
	},
	{
		key:       ']',
		label:     "Next",
		menuTitle: "Next",
		children: []command{
			{key: 'b', label: "Next buffer", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return NextBufferMsg{} }
			}},
		},
	},
	{
		key:       '[',
		label:     "Prev",
		menuTitle: "Prev",
		children: []command{
			{key: 'b', label: "Previous buffer", execute: func(m Model) (tea.Model, tea.Cmd) {
				return m, func() tea.Msg { return PrevBufferMsg{} }
			}},
		},
	},
	commandMenuRoot,
	{
		key:       '~',
		label:     "Case",
		menuTitle: "Case",
		children: []command{
			{key: 's', label: "snake_case", execute: executeCaseConvertSnake},
			{key: 'S', label: "SCREAMING_SNAKE_CASE", execute: executeCaseConvertScreamingSnake},
			{key: 'c', label: "camelCase", execute: executeCaseConvertCamel},
			{key: 'p', label: "PascalCase", execute: executeCaseConvertPascal},
			{key: 'k', label: "kebab-case", execute: executeCaseConvertKebab},
			{key: 'd', label: "dot.case", execute: executeCaseConvertDot},
		},
	},
	{
		key:       'M',
		label:     "Move",
		menuTitle: "Move",
		children: []command{
			{key: 'j', label: "Move line(s) down", execute: executeMoveLineDown},
			{key: 'k', label: "Move line(s) up", execute: executeMoveLineUp},
		},
	},
	{
		key:       'm',
		label:     "Match",
		menuTitle: "Match",
		children: []command{
			{key: 'm', label: "Go to matching bracket", execute: executeGotoMatchingBracket},
			{
				key:       'i',
				label:     "Select inside object",
				menuTitle: "Match Inside",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closest surrounding pair", execute: executeSelectInsideBrackets},
					{key: '.', label: "Quote/delimiter pair", execute: executeSelectInsideChar},
					{key: 'f', label: "Function", execute: executeSelectInsideFunction},
					{key: 't', label: "Type definition", execute: executeSelectInsideType},
					{key: 'a', label: "Argument/parameter", execute: executeSelectInsideArgument},
					{key: 'c', label: "Comment", execute: executeSelectInsideComment},
				},
			},
			{
				key:       'a',
				label:     "Select around object",
				menuTitle: "Match Around",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closest surrounding pair", execute: executeSelectAroundBrackets},
					{key: '.', label: "Quote/delimiter pair", execute: executeSelectAroundChar},
					{key: 'f', label: "Function", execute: executeSelectAroundFunction},
					{key: 't', label: "Type definition", execute: executeSelectAroundType},
					{key: 'a', label: "Argument/parameter", execute: executeSelectAroundArgument},
					{key: 'c', label: "Comment", execute: executeSelectAroundComment},
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
	key:       ' ',
	label:     "Command",
	menuTitle: "Command",
	children: []command{
		{key: 's', label: "Search & Replace", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenSearchReplaceMsg{} }
		}},
		{key: 'p', label: "Symbol picker", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenSymbolPickerMsg{BufID: m.bufID} }
		}},
		{key: 'f', label: "File picker", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenPickerMsg{} }
		}},
		{key: 'n', label: "New file", execute: func(m Model) (tea.Model, tea.Cmd) {
			return m, func() tea.Msg { return OpenNewFileMsg{} }
		}},
		{key: 'a', label: "Code Actions (fixes & refactors)", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Uses the current selection as the request range when one is
			// active, so range refactors (Extract Function/Variable) are
			// offered alongside point-based quick-fixes.
			return m, m.fetchFixes()
		}},
		{key: 'r', label: "Refactor: Rename Symbol", execute: func(m Model) (tea.Model, tea.Cmd) {
			// Uses the language server if one is running for this buffer;
			// see doRenameSymbol / EditorService.lspRename.
			m.mode = ModeCommand
			m.cmdBuf = "rename "
			m.cmdCompletionIdx = -1
			return m, nil
		}},
		{key: 'm', label: "Refactor: Move Function to File", execute: func(m Model) (tea.Model, tea.Cmd) {
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
func findCommand(seq []rune) (*command, bool) {
	return findIn(prefixCmds, seq)
}

// findIn walks cmds following seq and returns the final node, recursing into
// each matched node's children for the next rune in seq.
func findIn(cmds []command, seq []rune) (*command, bool) {
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
func (m Model) resolveCommand(seq []rune) (*command, bool) {
	if len(seq) == 0 || seq[0] != ' ' {
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

// keyRune picks the popup-selector rune for a plugin menu item: the declared
// Key if set, otherwise the lowercased first rune of Label.
func keyRune(key, label string) rune {
	if r := []rune(key); len(r) > 0 {
		return r[0]
	}
	for _, r := range strings.ToLower(label) {
		return r
	}
	return 0
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
		return pluginKeyResultMsg{result: result}
	}
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		helpLines := helpPopupLines(m.pluginBindings)
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
	m.status = ""
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
			m.status = fmt.Sprintf("E: bad path: %v", err)
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
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			content, err := m.rpc.DiscardRecovery(ctx, m.bufID)
			if err != nil {
				return errorMsg{err}
			}
			return discardRecoveryMsg{content}
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
		return pluginKeyResultMsg{result: result}
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
		return pluginKeyResultMsg{result: result}
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
