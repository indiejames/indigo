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

// exOnlyDisplay gives exOnlyActions' names a canonical label/category, for
// when one is bound to a key via [[keybind]] (rebindRoot) or listed by the
// generated help popup / --dump-keybinds — exOnlyActions itself only maps
// to a bare execute func, with nothing human-readable to show.
var exOnlyDisplay = map[string]displayInfo{
	"quit":             {label: "Quit (fails if unsaved)", category: "Files & Buffers"},
	"quit-force":       {label: "Quit, discarding changes", category: "Files & Buffers"},
	"write-quit":       {label: "Save and quit", category: "Files & Buffers"},
	"quit-all":         {label: "Quit all (fails if any unsaved)", category: "Files & Buffers"},
	"quit-all-force":   {label: "Quit all, discarding changes", category: "Files & Buffers"},
	"write-quit-all":   {label: "Save all and quit", category: "Files & Buffers"},
	"format":           {label: "Format buffer", category: "Editing"},
	"show-diagnostics": {label: "Open workspace diagnostic browser", category: "LSP / Diagnostics"},
	"toggle-metrics":   {label: "Toggle metrics overlay", category: "System"},
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
// the pristine, default prefixCmds tree (not the live, override-mutated
// one — see below) plus exOnlyActions.
//
// Built from defaultPrefixCmds rather than the live prefixCmds: rebindRoot
// (keybinds.go) overrides a key by overwriting that tree node's name and
// execute fields in place, rather than adding a new node. For a
// single-location action like "save" (only ctrl+s carries that name), a
// [[keybind]] that repoints ctrl+s at a different action erases "save" from
// the live tree entirely — which would make ":save" report "unknown
// command" purely because of an unrelated key rebind. A key override should
// only change what triggers an action, never whether the action's name is
// still resolvable from ":", so this resolves against the immutable
// default set instead.
func exActionRegistry() map[string]func(Model) (tea.Model, tea.Cmd) {
	reg := actionRegistry(defaultPrefixCmds)
	for name, fn := range exOnlyActions {
		reg[name] = fn
	}
	return reg
}
