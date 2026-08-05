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

// rebindRoot finds key among root-level entries of cmds and points it at fn
// (recording name so later lookups/overrides can find it too). A key that's
// currently a multi-key prefix menu is refused rather than silently
// collapsed into a single action. A key not found is appended as a new leaf.
func rebindRoot(cmds []command, key, name string, fn func(Model) (tea.Model, tea.Cmd)) ([]command, string) {
	for i := range cmds {
		if cmds[i].key != key {
			continue
		}
		if len(cmds[i].children) > 0 {
			return cmds, fmt.Sprintf("keybind: key %q is already the %q prefix menu and can't be rebound to a single action", key, cmds[i].label)
		}
		cmds[i].name = name
		cmds[i].label = name
		cmds[i].execute = fn
		return cmds, ""
	}
	return append(cmds, command{key: key, name: name, label: name, execute: fn}), ""
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
				modeLabel string
			)
			switch kb.Mode {
			case "normal":
				target, actions, modeLabel = &normal, normalActions, "normal"
			case "insert":
				target, actions, modeLabel = &insert, insertActions, "insert"
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
			*target, warn = rebindRoot(*target, kb.Key, kb.Action, fn)
			if warn != "" {
				warnings = append(warnings, warn)
			}
		}
	}

	prefixCmds = normal
	insertCmds = insert
	return warnings
}
