package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/client"
)

// tabGroup is a named, ordered set of buffers. The tab bar shows only the
// active group's tabs, so grouping is how a long tab list is kept short.
//
// Every buffer is in exactly one group. Groups name buffers by BufID rather
// than index for the reason tabUse does: opening and closing buffers
// renumbers a.buffers.
type tabGroup struct {
	name   string   // "" is the group of buffers never moved into a named one
	bufIDs []uint32 // tab order
}

// ungroupedLabel is what the bar and the pickers call the unnamed group.
const ungroupedLabel = "ungrouped"

func (g tabGroup) label() string {
	if g.name == "" {
		return ungroupedLabel
	}
	return g.name
}

// syncGroups makes the groups describe a.buffers: closed buffers are dropped,
// a group left empty is deleted, and a buffer in no group joins the current
// one. It then makes the current group the active buffer's.
//
// Run once per Update, so none of the places that open a buffer or assign
// a.active has to know about groups. That includes a jump into a file open
// in another group: the active buffer is in that group, so it becomes the
// current one, and the file stays where it was put.
func (a App) syncGroups() App {
	if len(a.buffers) == 0 {
		a.groups = nil
		a.groupName = ""
		return a
	}
	if !a.groupsConsistent() {
		live := make(map[uint32]bool, len(a.buffers))
		for _, m := range a.buffers {
			live[m.BufID()] = true
		}
		seen := make(map[uint32]bool, len(a.buffers))
		groups := make([]tabGroup, 0, len(a.groups)+1)
		for _, g := range a.groups {
			var ids []uint32
			for _, id := range g.bufIDs {
				if live[id] && !seen[id] {
					ids = append(ids, id)
					seen[id] = true
				}
			}
			if len(ids) > 0 {
				groups = append(groups, tabGroup{name: g.name, bufIDs: ids})
			}
		}
		a.groups = groups
		target := a.groupIndex(a.groupName)
		if target < 0 && a.active >= 0 && a.active < len(a.buffers) {
			target = a.groupOf(a.buffers[a.active].BufID())
		}
		for _, m := range a.buffers {
			if seen[m.BufID()] {
				continue
			}
			// A file with a group of its own — saved from an earlier run, or
			// where it was in this window before it was closed — goes back
			// there, even when that group has to be created again.
			if name, ok := a.placement[m.FilePath()]; ok && m.FilePath() != "" {
				gi := a.groupIndex(name)
				if gi < 0 {
					a.groups = append(a.groups, tabGroup{name: name})
					gi = len(a.groups) - 1
				}
				a.groups[gi].bufIDs = append(a.groups[gi].bufIDs, m.BufID())
				continue
			}
			if target < 0 {
				target = a.groupIndex("")
				if target < 0 {
					a.groups = append(a.groups, tabGroup{})
					target = len(a.groups) - 1
				}
			}
			a.groups[target].bufIDs = append(a.groups[target].bufIDs, m.BufID())
			a.place(m.FilePath(), a.groups[target].name)
		}
	}
	if a.active >= 0 && a.active < len(a.buffers) {
		if gi := a.groupOf(a.buffers[a.active].BufID()); gi >= 0 {
			a.groupName = a.groups[gi].name
		}
	}
	return a
}

// groupsConsistent reports whether the groups hold exactly the open buffers,
// with no empty group, so syncGroups can skip rebuilding them. A matching
// count with every buffer found means nothing is missing or extra, since
// BufIDs in a.buffers are unique.
func (a App) groupsConsistent() bool {
	n := 0
	for _, g := range a.groups {
		if len(g.bufIDs) == 0 {
			return false
		}
		n += len(g.bufIDs)
	}
	if n != len(a.buffers) {
		return false
	}
	for _, m := range a.buffers {
		if a.groupOf(m.BufID()) < 0 {
			return false
		}
	}
	return true
}

// groupOf returns the index of the group holding bufID, or -1.
func (a App) groupOf(bufID uint32) int {
	for gi, g := range a.groups {
		if slices.Contains(g.bufIDs, bufID) {
			return gi
		}
	}
	return -1
}

// groupIndex returns the index of the group called name, or -1.
func (a App) groupIndex(name string) int {
	for gi, g := range a.groups {
		if g.name == name {
			return gi
		}
	}
	return -1
}

// activeGroup returns the index of the active buffer's group, or -1 when
// there is none (no buffers, or groups not yet synced).
func (a App) activeGroup() int {
	if a.active < 0 || a.active >= len(a.buffers) {
		return -1
	}
	return a.groupOf(a.buffers[a.active].BufID())
}

// bufIndex returns the index in a.buffers of bufID, or -1.
func (a App) bufIndex(bufID uint32) int {
	for i, m := range a.buffers {
		if m.BufID() == bufID {
			return i
		}
	}
	return -1
}

// groupTabs returns the indices in a.buffers of the tabs the bar shows: the
// active group's, in its order. Before the groups have been synced — an App
// built by hand, as tests do — that is every buffer in buffer order, which
// is also what a single group holds.
func (a App) groupTabs() []int {
	gi := a.activeGroup()
	if gi < 0 {
		all := make([]int, len(a.buffers))
		for i := range all {
			all[i] = i
		}
		return all
	}
	out := make([]int, 0, len(a.groups[gi].bufIDs))
	for _, id := range a.groups[gi].bufIDs {
		if i := a.bufIndex(id); i >= 0 {
			out = append(out, i)
		}
	}
	return out
}

// showGroupStrip reports whether the tab bar shows the group names: when
// there is more than one group, or a single named one.
func (a App) showGroupStrip() bool {
	return len(a.groups) > 1 || (len(a.groups) == 1 && a.groups[0].name != "")
}

// cycleBuffer moves delta tabs along the active group, wrapping. Next and
// previous buffer stay inside the group, since leaving it would select a tab
// the bar doesn't show.
func (a App) cycleBuffer(delta int) App {
	tabs := a.groupTabs()
	if len(tabs) < 2 {
		return a
	}
	pos := slices.Index(tabs, a.active)
	if pos < 0 {
		pos = 0
	}
	a.active = tabs[((pos+delta)%len(tabs)+len(tabs))%len(tabs)]
	a.status = ""
	return a
}

// mostRecentIn returns the index in a.buffers of the most recently active of
// ids, or -1 when none is open. A buffer never focused counts as oldest, and
// ties go to the earlier id, so the answer is deterministic.
func (a App) mostRecentIn(ids []uint32) int {
	best, bestUse := -1, uint64(0)
	for _, id := range ids {
		i := a.bufIndex(id)
		if i < 0 {
			continue
		}
		if use := a.tabUse[id]; best < 0 || use > bestUse {
			best, bestUse = i, use
		}
	}
	return best
}

// switchToGroup makes group gi current, on its most recently active buffer.
func (a App) switchToGroup(gi int) App {
	if gi < 0 || gi >= len(a.groups) {
		return a
	}
	if i := a.mostRecentIn(a.groups[gi].bufIDs); i >= 0 {
		a.active = i
		a.groupName = a.groups[gi].name
		a.status = ""
	}
	return a
}

// cycleGroup moves delta groups along the group list, wrapping.
func (a App) cycleGroup(delta int) App {
	if len(a.groups) < 2 {
		return a
	}
	gi := max(a.activeGroup(), 0)
	return a.switchToGroup(((gi+delta)%len(a.groups) + len(a.groups)) % len(a.groups))
}

// closeSuccessor returns the BufID to show once the active buffer closes: its
// right-hand neighbour in its group, else its left-hand one, else the most
// recently active buffer of another group. ok is false when it is the last
// buffer open.
func (a App) closeSuccessor() (uint32, bool) {
	closing := a.buffers[a.active].BufID()
	if gi := a.groupOf(closing); gi >= 0 {
		ids := a.groups[gi].bufIDs
		if p := slices.Index(ids, closing); p >= 0 {
			if p+1 < len(ids) {
				return ids[p+1], true
			}
			if p > 0 {
				return ids[p-1], true
			}
		}
	}
	others := make([]uint32, 0, len(a.buffers))
	for _, m := range a.buffers {
		if m.BufID() != closing {
			others = append(others, m.BufID())
		}
	}
	if i := a.mostRecentIn(others); i >= 0 {
		return a.buffers[i].BufID(), true
	}
	return 0, false
}

// moveToGroup moves bufID into the group called name and makes it the active
// buffer, so its group becomes the current one.
func (a App) moveToGroup(bufID uint32, name string) App {
	a = a.placeBuffer(bufID, name)
	if i := a.bufIndex(bufID); i >= 0 {
		a.active = i
	}
	a.groupName = name
	return a.syncGroups()
}

// placeBuffer moves bufID to the end of the group called name, creating the
// group when there is none, and records the group for its file (placement).
// The active buffer doesn't change. A group the move empties is deleted by
// the next syncGroups.
func (a App) placeBuffer(bufID uint32, name string) App {
	groups := make([]tabGroup, 0, len(a.groups)+1)
	for _, g := range a.groups {
		g.bufIDs = slices.DeleteFunc(slices.Clone(g.bufIDs), func(id uint32) bool { return id == bufID })
		groups = append(groups, g)
	}
	a.groups = groups
	gi := a.groupIndex(name)
	if gi < 0 {
		a.groups = append(a.groups, tabGroup{name: name})
		gi = len(a.groups) - 1
	}
	a.groups[gi].bufIDs = append(a.groups[gi].bufIDs, bufID)
	if i := a.bufIndex(bufID); i >= 0 {
		a.place(a.buffers[i].FilePath(), name)
	}
	return a.syncGroups()
}

// place records that the file at path belongs in the group called name, so it
// goes back there whenever it is opened again in this window. Untitled
// buffers have no path to remember.
func (a *App) place(path, name string) {
	if path == "" {
		return
	}
	if a.placement == nil {
		a.placement = make(map[string]string)
	}
	a.placement[path] = name
}

// tabGroupNamedMsg carries a name typed into the new-group or rename prompt.
type tabGroupNamedMsg struct {
	rename bool
	bufID  uint32 // new: the buffer to move into the group
	from   string // rename: the group's current name
	name   string
}

// handleTabGroup acts on a TabGroupMsg from the active buffer.
func (a App) handleTabGroup(msg client.TabGroupMsg) (tea.Model, tea.Cmd) {
	if len(a.buffers) == 0 {
		return a, nil
	}
	a = a.syncGroups()
	bufID := a.buffers[a.active].BufID()
	switch msg.Action {
	case client.TabGroupNew:
		a.pluginInput = a.groupNamePrompt("New Tab Group", "", func(name string) tea.Msg {
			return tabGroupNamedMsg{bufID: bufID, name: name}
		})
	case client.TabGroupRename:
		from := a.groupName
		a.pluginInput = a.groupNamePrompt("Rename Tab Group", from, func(name string) tea.Msg {
			return tabGroupNamedMsg{rename: true, from: from, name: name}
		})
	case client.TabGroupMove:
		a.groupPicker = a.newGroupPicker(true, bufID)
	case client.TabGroupSwitch:
		if len(a.groups) < 2 {
			a.status = "Only one tab group (space t n starts another)"
			return a, nil
		}
		a.groupPicker = a.newGroupPicker(false, bufID)
	case client.TabGroupClose:
		return a.closeGroup(a.activeGroup())
	case client.TabGroupNext:
		a = a.cycleGroup(1)
		return a, a.buffers[a.active].ReportActiveContextCmd()
	case client.TabGroupPrev:
		a = a.cycleGroup(-1)
		return a, a.buffers[a.active].ReportActiveContextCmd()
	}
	return a, nil
}

// groupNamePrompt is the App's own text prompt, prefilled with text.
func (a App) groupNamePrompt(title, text string, done func(name string) tea.Msg) *appPluginInput {
	return &appPluginInput{
		title:       title,
		placeholder: "group name",
		text:        text,
		width:       a.width,
		height:      a.height,
		onConfirm: func(text string) tea.Cmd {
			return func() tea.Msg { return done(strings.TrimSpace(text)) }
		},
	}
}

// handleTabGroupNamed applies a name typed into a group prompt. Naming a new
// group after an existing one moves the buffer into that group, which is what
// someone typing its name wants.
func (a App) handleTabGroupNamed(msg tabGroupNamedMsg) (tea.Model, tea.Cmd) {
	if msg.name == "" {
		a.status = "E: a tab group needs a name"
		return a, nil
	}
	if !msg.rename {
		if a.bufIndex(msg.bufID) < 0 {
			return a, nil
		}
		a = a.moveToGroup(msg.bufID, msg.name)
		return a, a.resizeAllBuffers()
	}
	gi := a.groupIndex(msg.from)
	if gi < 0 || msg.name == msg.from {
		return a, nil
	}
	if a.groupIndex(msg.name) >= 0 {
		a.status = fmt.Sprintf("E: there is already a tab group called %q", msg.name)
		return a, nil
	}
	a.groups = slices.Clone(a.groups)
	a.groups[gi].name = msg.name
	if a.groupName == msg.from {
		a.groupName = msg.name
	}
	for path, name := range a.placement {
		if name == msg.from {
			a.placement[path] = msg.name
		}
	}
	// A first name for the ungrouped buffers can bring the strip, and with it
	// the tab bar, into view.
	return a, a.resizeAllBuffers()
}

// closeGroup closes every buffer in group gi, refusing while any of them has
// unsaved changes. Closing the only group closes the window, as closing the
// last buffer does.
func (a App) closeGroup(gi int) (tea.Model, tea.Cmd) {
	if gi < 0 || gi >= len(a.groups) {
		return a, nil
	}
	g := a.groups[gi]
	dirty := 0
	for _, id := range g.bufIDs {
		if i := a.bufIndex(id); i >= 0 && a.buffers[i].Dirty() {
			dirty++
		}
	}
	if dirty > 0 {
		a.status = fmt.Sprintf("E: %d unsaved file(s) in tab group %q", dirty, g.label())
		return a, nil
	}
	if len(g.bufIDs) == len(a.buffers) {
		return a, a.doCloseAllAndQuit()
	}
	closing := slices.Clone(g.bufIDs)
	kept := make([]client.Model, 0, len(a.buffers)-len(closing))
	for _, m := range a.buffers {
		if slices.Contains(closing, m.BufID()) {
			a.forgetTabUse(m.BufID())
			// The group is gone, so a file of it opened again joins the
			// current group rather than bringing the closed one back.
			delete(a.placement, m.FilePath())
		} else {
			kept = append(kept, m)
		}
	}
	a.buffers = kept
	a.active = 0
	others := make([]uint32, len(kept))
	for i, m := range kept {
		others[i] = m.BufID()
	}
	if i := a.mostRecentIn(others); i >= 0 {
		a.active = i
	}
	a.groupName = ""
	a = a.syncGroups()
	rpc := a.rpc
	closeCmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range closing {
			rpc.CloseBuffer(ctx, id) //nolint:errcheck
		}
		return nil
	}
	return a, tea.Batch(closeCmd, a.resizeAllBuffers(), a.buffers[a.active].ReportActiveContextCmd())
}

// groupPicker is the list popup for switching groups and for choosing where
// to move a buffer.
type groupPicker struct {
	move   bool   // true: move bufID into the chosen group; false: switch to it
	bufID  uint32 // the buffer being moved
	items  []groupPickerItem
	cursor int
	width  int
	height int
}

type groupPickerItem struct {
	label string
	name  string // the group's name; unused for the new-group entry
	isNew bool   // "New group…": prompt for a name instead
}

const newGroupLabel = "New group…"

func (a App) newGroupPicker(move bool, bufID uint32) *groupPicker {
	gp := &groupPicker{move: move, bufID: bufID, width: a.width, height: a.height}
	current := a.activeGroup()
	for gi, g := range a.groups {
		if move && gi == current {
			continue // moving a buffer to the group it is in does nothing
		}
		gp.items = append(gp.items, groupPickerItem{label: fmt.Sprintf("%s (%d)", g.label(), len(g.bufIDs)), name: g.name})
		if gi == current {
			gp.cursor = len(gp.items) - 1
		}
	}
	if move {
		gp.items = append(gp.items, groupPickerItem{label: newGroupLabel, isNew: true})
	}
	return gp
}

// handleGroupPickerKey routes key events to the group picker.
func (a App) handleGroupPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	gp := a.groupPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.groupPicker = nil
	case "up", "k":
		if gp.cursor > 0 {
			gp.cursor--
		}
	case "down", "j":
		if gp.cursor < len(gp.items)-1 {
			gp.cursor++
		}
	case "enter":
		a.groupPicker = nil
		if len(gp.items) == 0 {
			return a, nil
		}
		item := gp.items[gp.cursor]
		switch {
		case item.isNew:
			return a.handleTabGroup(client.TabGroupMsg{Action: client.TabGroupNew})
		case gp.move:
			if a.bufIndex(gp.bufID) < 0 {
				return a, nil
			}
			a = a.moveToGroup(gp.bufID, item.name)
			return a, a.resizeAllBuffers()
		default:
			a = a.switchToGroup(a.groupIndex(item.name))
			return a, a.buffers[a.active].ReportActiveContextCmd()
		}
	}
	return a, nil
}

func (gp *groupPicker) render() string {
	title := "Switch Tab Group"
	if gp.move {
		title = "Move Buffer to Tab Group"
	}
	innerW := lipgloss.Width(title)
	for _, item := range gp.items {
		innerW = max(innerW, lipgloss.Width(item.label))
	}
	innerW = max(min(innerW+4, gp.width*2/3), 30)

	vis := visibleRows(len(gp.items), bufPickerMaxVisible, gp.height, 4)
	start := max(0, min(gp.cursor-vis/2, len(gp.items)-vis))
	end := min(start+vis, len(gp.items))

	rows := []string{
		bufPickerTitleStyle.Width(innerW).Render(title),
		lipgloss.NewStyle().Background(bufPickerBg).Foreground(lipgloss.Color("#4488CC")).Render(strings.Repeat("─", innerW)),
	}
	for i := start; i < end; i++ {
		style := bufPickerItemStyle
		if i == gp.cursor {
			style = bufPickerSelStyle
		}
		rows = append(rows, style.Width(innerW).Render(gp.items[i].label))
	}
	return bufPickerBorderStyle.Render(strings.Join(rows, "\n"))
}
