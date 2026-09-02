package client

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/config"
)

// applyKeymenus merges cfg's [[keymenu]] entries into cmds (a Normal-mode
// tree under construction — see applyKeybindOverrides), returning the
// updated tree and a warning per entry that's structurally invalid or
// shadows an existing root-level binding. display/actions are the same
// merged registries applyKeybindOverrides already built for [[keybind]]
// validation — a keymenu leaf's action is looked up the identical way.
func applyKeymenus(cmds []command, keymenus []config.KeymenuNode, display map[string]displayInfo, actions map[string]func(Model) (tea.Model, tea.Cmd)) ([]command, []string) {
	var warnings []string
	for _, km := range keymenus {
		if km.Key == "" {
			warnings = append(warnings, fmt.Sprintf("keymenu: empty key for menu %q", km.Label))
			continue
		}
		node, warns := buildKeymenuCommand(km, km.Key, display, actions)
		warnings = append(warnings, warns...)
		if node == nil {
			continue
		}
		replaced := false
		for i := range cmds {
			if cmds[i].key == km.Key {
				warnings = append(warnings, fmt.Sprintf(
					"keymenu: %q shadows the existing %q binding — it is now unreachable at %q",
					km.Key, cmds[i].label, km.Key))
				cmds[i] = *node
				replaced = true
				break
			}
		}
		if !replaced {
			cmds = append(cmds, *node)
		}
	}
	return cmds, warnings
}

// buildKeymenuCommand recursively converts a config.KeymenuNode into a
// command, validating structure as it goes: a node must be either a leaf
// (Action set) or a branch (one or more Children), never both or neither;
// no two siblings may share a Key. path accumulates as "parentKey
// childKey" purely to make warnings identify which node is at fault. A
// child that fails validation is dropped (with its own warnings surfaced)
// without affecting its siblings.
func buildKeymenuCommand(node config.KeymenuNode, path string, display map[string]displayInfo, actions map[string]func(Model) (tea.Model, tea.Cmd)) (*command, []string) {
	var warnings []string
	hasAction := node.Action != ""
	hasChildren := len(node.Children) > 0

	if hasAction && hasChildren {
		return nil, []string{fmt.Sprintf("keymenu: %q has both an action and child entries — a node must be one or the other", path)}
	}
	if !hasAction && !hasChildren {
		return nil, []string{fmt.Sprintf("keymenu: %q has neither an action nor child entries", path)}
	}

	if hasAction {
		fn, ok := actions[node.Action]
		if !ok {
			return nil, []string{fmt.Sprintf("keymenu: %q: unknown action %q", path, node.Action)}
		}
		label := node.Label
		if label == "" {
			if info, ok := display[node.Action]; ok {
				label = info.label
			} else {
				label = node.Action
			}
		}
		return &command{key: node.Key, name: node.Action, label: label, execute: fn}, nil
	}

	seen := map[string]bool{}
	var children []command
	for _, c := range node.Children {
		if c.Key == "" {
			warnings = append(warnings, fmt.Sprintf("keymenu: %q has a child with an empty key", path))
			continue
		}
		if seen[c.Key] {
			warnings = append(warnings, fmt.Sprintf("keymenu: %q has more than one child with key %q — only the first is kept", path, c.Key))
			continue
		}
		seen[c.Key] = true
		childPath := path + " " + c.Key
		child, warns := buildKeymenuCommand(c, childPath, display, actions)
		warnings = append(warnings, warns...)
		if child != nil {
			children = append(children, *child)
		}
	}
	if len(children) == 0 {
		warnings = append(warnings, fmt.Sprintf("keymenu: %q has no valid child entries", path))
		return nil, warnings
	}

	label := node.Label
	if label == "" {
		label = node.Key
	}
	return &command{key: node.Key, label: label, menuTitle: label, category: label, children: children}, warnings
}
