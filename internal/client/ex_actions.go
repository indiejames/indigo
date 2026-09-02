package client

import tea "github.com/charmbracelet/bubbletea"

// exCommandAliases maps every literal, argument-free ":" command token to
// the stable action name it resolves to in exActionRegistry. Multiple
// tokens legitimately alias the same action (vim-ish shorthands alongside
// full words); this table is the one place that mapping is declared.
var exCommandAliases = map[string]string{
	"w": "save", "write": "save", "s": "save", "save": "save",
	"q": "quit", "quit": "quit",
	"q!": "quit-force", "quit!": "quit-force",
	"wq": "write-quit", "x": "write-quit", "write-quit": "write-quit",
	"qa": "quit-all", "quit-all": "quit-all",
	"qa!": "quit-all-force", "quit-all!": "quit-all-force",
	"wqa": "write-quit-all",
	"e":   "open-file-picker", "edit": "open-file-picker",
	"new": "new-file",
	"fmt": "format", "format": "format",
	"diagnostics": "show-diagnostics", "diag": "show-diagnostics",
	"metrics": "toggle-metrics",
}

// exOnlyActions are zero-argument actions reachable only via ":" today —
// they have no keypress or menu equivalent. Merged into the live
// keypress-tree action registry by exActionRegistry, and into
// applyKeybindOverrides' per-mode action maps (keybinds.go), so they become
// ordinary [[keybind]] targets for free.
var exOnlyActions = map[string]func(Model) (tea.Model, tea.Cmd){
	"quit":       executeExQuit,
	"quit-force": func(m Model) (tea.Model, tea.Cmd) { return m, m.doCloseBuffer() },
	"write-quit": func(m Model) (tea.Model, tea.Cmd) { return m, m.doSaveAndClose() },
	"quit-all":   func(m Model) (tea.Model, tea.Cmd) { return m, func() tea.Msg { return QuitAllMsg{} } },
	"quit-all-force": func(m Model) (tea.Model, tea.Cmd) {
		return m, func() tea.Msg { return QuitAllMsg{Force: true} }
	},
	"write-quit-all": func(m Model) (tea.Model, tea.Cmd) {
		return m, func() tea.Msg { return QuitAllMsg{SaveAll: true} }
	},
	"format": func(m Model) (tea.Model, tea.Cmd) { return m, m.fetchFormat(false) },
	"show-diagnostics": func(m Model) (tea.Model, tea.Cmd) {
		return m, func() tea.Msg { return OpenDiagnosticBrowserMsg{} }
	},
	"toggle-metrics": func(m Model) (tea.Model, tea.Cmd) {
		if m.metrics != nil {
			m.metrics.show = !m.metrics.show
		}
		return m, nil
	},
}

// executeExQuit is ":q"/":quit": close the buffer unless it has unsaved
// changes, matching the pre-unification switch's dirty-buffer guard.
func executeExQuit(m Model) (tea.Model, tea.Cmd) {
	if m.buf.Dirty() {
		m = m.pushStatus("E: unsaved changes (use :q! to discard)")
		return m, nil
	}
	return m, m.doCloseBuffer()
}

// exActionRegistry returns the merged action registry: every named leaf in
// the live prefixCmds tree (reflecting any [[keybind]] overrides currently
// applied) plus exOnlyActions. Recomputed on each call rather than cached —
// the tree is small and this keeps ":" dispatch trivially correct across
// config hot-reload with no separate invalidation path to get wrong.
func exActionRegistry() map[string]func(Model) (tea.Model, tea.Cmd) {
	reg := actionRegistry(prefixCmds)
	for name, fn := range exOnlyActions {
		reg[name] = fn
	}
	return reg
}
