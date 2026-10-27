package client

import (
	"fmt"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

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

// applyKeybindOverrides rebuilds insertCmds, the keymap, from its pristine
// default plus cfg's [[keybind]] and [[keymenu]] entries, returning a
// human-readable warning per entry it could not apply. Called from Model.New
// and Model.WithConfig so both initial load and config hot-reload pick up
// changes.
//
// There is one keymap, so a [[keybind]] leaves mode out (or says "insert",
// what it had to say before). One that says "normal" is refused with a
// warning rather than bound, because a Normal-mode key is usually a letter,
// and bound in the keymap it would stop that letter being typed. A [[keymenu]]
// becomes a menu in the keymap, opened by its key.
func applyKeybindOverrides(cfg *config.Config) []string {
	keymap := append([]command(nil), defaultInsertCmds...)
	// Any action can be bound, wherever it is otherwise reached: the keymap,
	// a menu, the palette.
	actions := exActionRegistry()
	display := mergeDisplayInfo(mergeDisplayInfo(canonicalDisplayInfo(defaultPrefixCmds), canonicalDisplayInfo(defaultInsertCmds)), exOnlyDisplay)

	var warnings []string
	if cfg != nil {
		for _, kb := range cfg.Keybinds {
			if kb.Key == "" {
				warnings = append(warnings, fmt.Sprintf("keybind: empty key for action %q", kb.Action))
				continue
			}
			switch kb.Mode {
			case "", "insert":
			case "normal":
				warnings = append(warnings, fmt.Sprintf("keybind: %q is for Normal mode, which no longer exists; leave mode out to bind it in the keymap", kb.Key))
				continue
			default:
				warnings = append(warnings, fmt.Sprintf("keybind: unknown mode %q (leave it out)", kb.Mode))
				continue
			}
			if typedKey(kb.Key) {
				warnings = append(warnings, fmt.Sprintf("keybind: %q types a character, so it can't be bound to an action", kb.Key))
				continue
			}
			fn, ok := actions[kb.Action]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("keybind: unknown action %q", kb.Action))
				continue
			}
			var warn string
			keymap, warn = rebindRoot(keymap, kb.Key, kb.Action, fn, display)
			if warn != "" {
				warnings = append(warnings, warn)
			}
		}
	}

	// [[keymenu]] is applied after [[keybind]], so a menu leaf can name an
	// action a [[keybind]] just bound as well as a built-in one.
	if cfg != nil {
		var menus []config.KeymenuNode
		for _, km := range cfg.Keymenus {
			if typedKey(km.Key) {
				warnings = append(warnings, fmt.Sprintf("keymenu: %q types a character, so it can't open a menu", km.Key))
				continue
			}
			menus = append(menus, km)
		}
		var kwarn []string
		keymap, kwarn = applyKeymenus(keymap, menus, display, actions)
		warnings = append(warnings, kwarn...)
	}

	insertCmds = keymap
	return warnings
}

// typedKey reports whether key, as Bubble Tea names it, types a character
// rather than being a chord or a named key: a single character, or space.
func typedKey(key string) bool {
	return key == "space" || utf8.RuneCountInString(key) == 1
}
