package client

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/config"
)

// defaultPrefixCmds and defaultInsertCmds are pristine copies of the
// built-in Normal/Insert key bindings, captured once before any config
// overrides are ever applied. applyKeybindOverrides rebuilds prefixCmds/
// insertCmds from these snapshots on every call (including config hot-reload
// via WithConfig) rather than mutating them cumulatively, so a config change
// that removes an override actually reverts to the built-in binding instead
// of leaving the previous override in place.
var (
	defaultPrefixCmds = append([]command(nil), prefixCmds...)
	defaultInsertCmds = append([]command(nil), insertCmds...)
)

// actionRegistry walks cmds (recursing into children) and returns a map of
// every named leaf's action name to its execute function.
func actionRegistry(cmds []command) map[string]func(Model) (tea.Model, tea.Cmd) {
	reg := make(map[string]func(Model) (tea.Model, tea.Cmd))
	var walk func([]command)
	walk = func(cs []command) {
		for _, c := range cs {
			if c.execute != nil && c.name != "" {
				reg[c.name] = c.execute
			}
			if len(c.children) > 0 {
				walk(c.children)
			}
		}
	}
	walk(cmds)
	return reg
}

// displayInfo pairs a canonical label with the help-popup category an
// action resolves to (see help_gen.go).
type displayInfo struct {
	label    string
	category string
}

// canonicalDisplayInfo walks cmds (recursing into children, inheriting each
// node's category down to descendants that don't set their own) and
// returns every named leaf's canonical (label, category). The first
// occurrence in tree order wins when a name appears more than once —
// TestNoDuplicateActionLabels guarantees every occurrence agrees on label
// anyway, so this is just a deterministic pick, not a real choice. Used by
// rebindRoot (so a [[keybind]] override displays the action's real label,
// not the bare action name) and by the generated help popup.
func canonicalDisplayInfo(cmds []command) map[string]displayInfo {
	reg := make(map[string]displayInfo)
	var walk func([]command, string)
	walk = func(cs []command, inherited string) {
		for _, c := range cs {
			cat := inherited
			if c.category != "" {
				cat = c.category
			}
			if c.execute != nil && c.name != "" {
				if _, exists := reg[c.name]; !exists {
					reg[c.name] = displayInfo{label: c.label, category: cat}
				}
			}
			if len(c.children) > 0 {
				walk(c.children, cat)
			}
		}
	}
	walk(cmds, "")
	return reg
}

// mergeDisplayInfo returns a new map containing every entry of base plus
// every entry of extra (extra wins on key collision).
func mergeDisplayInfo(base, extra map[string]displayInfo) map[string]displayInfo {
	out := make(map[string]displayInfo, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// rebindRoot finds key among root-level entries of cmds and points it at fn
// (recording name so later lookups/overrides can find it too), stamping the
// canonical label/category resolved from display so the entry reads like a
// built-in one rather than showing the bare action name. A key that's
// currently a multi-key prefix menu is refused rather than silently
// collapsed into a single action. A key not found is appended as a new leaf.
func rebindRoot(cmds []command, key, name string, fn func(Model) (tea.Model, tea.Cmd), display map[string]displayInfo) ([]command, string) {
	info, ok := display[name]
	label, category := name, ""
	if ok {
		label, category = info.label, info.category
	}
	for i := range cmds {
		if cmds[i].key != key {
			continue
		}
		if len(cmds[i].children) > 0 {
			return cmds, fmt.Sprintf("keybind: key %q is already the %q prefix menu and can't be rebound to a single action", key, cmds[i].label)
		}
		cmds[i].name = name
		cmds[i].label = label
		cmds[i].category = category
		cmds[i].execute = fn
		return cmds, ""
	}
	return append(cmds, command{key: key, name: name, label: label, category: category, execute: fn}), ""
}

// applyKeybindOverrides rebuilds prefixCmds and insertCmds from their
// pristine defaults plus cfg's [[keybind]] entries, returning a
// human-readable warning per entry that names an unknown mode/action or an
// empty key. Called from Model.New and Model.WithConfig so both initial load
// and config hot-reload pick up changes.
func applyKeybindOverrides(cfg *config.Config) []string {
	normal := append([]command(nil), defaultPrefixCmds...)
	insert := append([]command(nil), defaultInsertCmds...)
	normalActions := actionRegistry(defaultPrefixCmds)
	insertActions := actionRegistry(defaultInsertCmds)
	// Ex-only actions (no keypress/menu equivalent — see ex_actions.go) are
	// valid [[keybind]] targets too, in both modes, for the same reason a
	// tree-derived action is: there's no structural reason to forbid e.g.
	// binding "quit-force" to a key from Insert mode.
	for name, fn := range exOnlyActions {
		normalActions[name] = fn
		insertActions[name] = fn
	}
	normalDisplay := mergeDisplayInfo(canonicalDisplayInfo(defaultPrefixCmds), exOnlyDisplay)
	insertDisplay := mergeDisplayInfo(canonicalDisplayInfo(defaultInsertCmds), exOnlyDisplay)

	var warnings []string
	if cfg != nil {
		for _, kb := range cfg.Keybinds {
			if kb.Key == "" {
				warnings = append(warnings, fmt.Sprintf("keybind: empty key for action %q", kb.Action))
				continue
			}
			var (
				target    *[]command
				actions   map[string]func(Model) (tea.Model, tea.Cmd)
				display   map[string]displayInfo
				modeLabel string
			)
			switch kb.Mode {
			case "normal":
				target, actions, display, modeLabel = &normal, normalActions, normalDisplay, "normal"
			case "insert":
				target, actions, display, modeLabel = &insert, insertActions, insertDisplay, "insert"
			default:
				warnings = append(warnings, fmt.Sprintf("keybind: unknown mode %q (must be \"normal\" or \"insert\")", kb.Mode))
				continue
			}
			fn, ok := actions[kb.Action]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("keybind: unknown action %q for mode %q", kb.Action, modeLabel))
				continue
			}
			var warn string
			*target, warn = rebindRoot(*target, kb.Key, kb.Action, fn, display)
			if warn != "" {
				warnings = append(warnings, warn)
			}
		}
	}

	// [[keymenu]] is applied after [[keybind]] finishes mutating normal, so
	// a keymenu leaf's action can reference either a built-in action or one
	// a [[keybind]] override just renamed/introduced — one consistent
	// ordering. Normal mode only: Insert mode has no menu concept today.
	if cfg != nil {
		var kwarn []string
		normal, kwarn = applyKeymenus(normal, cfg.Keymenus, normalDisplay, normalActions)
		warnings = append(warnings, kwarn...)
	}

	prefixCmds = normal
	insertCmds = insert
	return warnings
}
