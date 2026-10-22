package client

import tea "charm.land/bubbletea/v2"

// TabGroupAction is what a TabGroupMsg asks the App to do. Tab groups live in
// the App, beside the tab list they organize, so the buffer only names the
// action.
type TabGroupAction int

const (
	TabGroupNew    TabGroupAction = iota // prompt for a name, move the current buffer into a new group
	TabGroupMove                         // pick a group to move the current buffer into
	TabGroupSwitch                       // pick a group to switch to
	TabGroupRename                       // rename the current group
	TabGroupClose                        // close every buffer in the current group
	TabGroupNext                         // switch to the next group
	TabGroupPrev                         // switch to the previous group
)

// TabGroupMsg signals the App to act on its tab groups.
type TabGroupMsg struct{ Action TabGroupAction }

func tabGroupCmd(action TabGroupAction) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		return m, func() tea.Msg { return TabGroupMsg{Action: action} }
	}
}

// tabGroupMenu is the Command menu's "t" submenu.
var tabGroupMenu = command{
	key:       "t",
	label:     "Tab groups",
	menuTitle: "Tab Groups",
	children: []command{
		{key: "n", name: "new-tab-group", label: "New group with this buffer…", execute: tabGroupCmd(TabGroupNew)},
		{key: "m", name: "move-to-tab-group", label: "Move this buffer to a group…", execute: tabGroupCmd(TabGroupMove)},
		{key: "t", name: "switch-tab-group", label: "Switch group…", execute: tabGroupCmd(TabGroupSwitch)},
		{key: "r", name: "rename-tab-group", label: "Rename this group…", execute: tabGroupCmd(TabGroupRename)},
		{key: "c", name: "close-tab-group", label: "Close this group", execute: tabGroupCmd(TabGroupClose)},
	},
}

var (
	executeNextTabGroup = tabGroupCmd(TabGroupNext)
	executePrevTabGroup = tabGroupCmd(TabGroupPrev)
)
