package app

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// newGroupApp builds an App with buffers 1..len(names), at /tmp/<name>, the
// first active, its groups synced as Update would leave them.
func newGroupApp(names ...string) App {
	buffers := make([]client.Model, len(names))
	for i, n := range names {
		buffers[i] = newReloadTestModel(uint32(i+1), "/tmp/"+n)
	}
	a := App{buffers: buffers, width: 120, height: 24, cfg: &config.Config{}, tabUse: map[uint32]uint64{}, fileChangedIdx: -1}
	return a.syncGroups().stampActiveTab()
}

func step(t *testing.T, a App, msg tea.Msg) (App, tea.Cmd) {
	t.Helper()
	next, cmd := a.Update(msg)
	got, ok := next.(App)
	if !ok {
		t.Fatalf("Update(%T) returned %T, want App", msg, next)
	}
	return got, cmd
}

// confirm presses Enter on a prompt and delivers the message its command
// produces, as Bubble Tea would.
func confirm(t *testing.T, a App) App {
	t.Helper()
	a, cmd := step(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on the prompt produced no command")
	}
	a, _ = step(t, a, cmd())
	return a
}

// groupsOf describes the groups as "name:1,2 other:3" for comparison.
func groupsOf(a App) string {
	var parts []string
	for _, g := range a.groups {
		ids := make([]string, len(g.bufIDs))
		for i, id := range g.bufIDs {
			ids[i] = fmt.Sprint(id)
		}
		parts = append(parts, g.label()+":"+strings.Join(ids, ","))
	}
	return strings.Join(parts, " ")
}

// newGroupNamed moves the active buffer into a new group through the menu
// action and the name prompt, the way a user does.
func newGroupNamed(t *testing.T, a App, name string) App {
	t.Helper()
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupNew})
	if a.pluginInput == nil {
		t.Fatal("new group: no name prompt")
	}
	return confirm(t, typeText(t, a, name))
}

func activeID(a App) uint32 { return a.buffers[a.active].BufID() }

func shownTabs(a App) []uint32 {
	slots, _ := a.tabLayout()
	out := make([]uint32, len(slots))
	for i, s := range slots {
		out[i] = a.buffers[s.idx].BufID()
	}
	return out
}

// TestWithoutGroupsTheBarIsUnchanged: every buffer starts in the one unnamed
// group, which shows no strip, so someone who never uses groups sees the bar
// as before.
func TestWithoutGroupsTheBarIsUnchanged(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	if got := groupsOf(a); got != "ungrouped:1,2,3" {
		t.Fatalf("groups = %q", got)
	}
	if a.showGroupStrip() {
		t.Error("the strip is shown with no named group")
	}
	if !strings.HasPrefix(ansi.Strip(a.renderTabBar()), "  a.go  ") {
		t.Errorf("bar = %q, want it to start with the first tab", ansi.Strip(a.renderTabBar()))
	}
	one := newGroupApp("a.go")
	if one.showTabBar() {
		t.Error("a single ungrouped buffer shows a tab bar")
	}
}

func TestNewGroupMovesTheBufferAndShowsOnlyItsTabs(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a, _ = step(t, a, switchBufferMsg{idx: 1, line: -1, col: -1})
	a = newGroupNamed(t, a, "api")

	if got := groupsOf(a); got != "ungrouped:1,3 api:2" {
		t.Fatalf("groups = %q", got)
	}
	if activeID(a) != 2 || a.groupName != "api" {
		t.Errorf("active buffer %d in group %q, want 2 in api", activeID(a), a.groupName)
	}
	if got := shownTabs(a); !slices.Equal(got, []uint32{2}) {
		t.Errorf("tabs shown = %v, want only api's", got)
	}
	bar := ansi.Strip(a.renderTabBar())
	if !strings.HasPrefix(bar, " ungrouped  api │  b.go  ") {
		t.Errorf("bar = %q", bar)
	}
	// A single buffer in a named group still shows its group.
	one := newGroupNamed(t, newGroupApp("a.go"), "solo")
	if !one.showTabBar() {
		t.Error("a named group with one buffer shows no tab bar")
	}
}

func TestAnOpenedBufferJoinsTheCurrentGroup(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, bufferOpenedMsg{model: newReloadTestModel(9, "/tmp/z.go"), line: -1, col: -1})
	if got := groupsOf(a); got != "ungrouped:2 api:1,9" {
		t.Errorf("groups = %q, want the new buffer in api", got)
	}
}

// TestJumpingIntoAnotherGroupSwitchesToIt: any path that makes a buffer in
// another group active — here the buffer picker — makes its group current,
// and leaves the buffer where it was.
func TestJumpingIntoAnotherGroupSwitchesToIt(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a = newGroupNamed(t, a, "api") // a.go
	a, _ = step(t, a, bufPickedMsg{idx: 2})
	if a.groupName != "" || activeID(a) != 3 {
		t.Fatalf("after picking c.go: group %q, buffer %d", a.groupName, activeID(a))
	}
	if got := shownTabs(a); !slices.Equal(got, []uint32{2, 3}) {
		t.Errorf("tabs shown = %v, want the ungrouped ones", got)
	}
	if got := groupsOf(a); got != "ungrouped:2,3 api:1" {
		t.Errorf("groups = %q, the jump moved a buffer", got)
	}
}

func TestNextAndPreviousBufferStayInTheGroup(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go", "d.go")
	a = newGroupNamed(t, a, "g") // a.go
	a, _ = step(t, a, bufPickedMsg{idx: 2})
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupMove})
	a = confirm(t, a) // the only other group, g
	// g holds 1 and 3; cycling must not visit 2 or 4.
	var seen []uint32
	for range 3 {
		a, _ = step(t, a, client.NextBufferMsg{})
		seen = append(seen, activeID(a))
	}
	if !slices.Equal(seen, []uint32{1, 3, 1}) {
		t.Errorf("next buffer visited %v, want 1 3 1", seen)
	}
	a, _ = step(t, a, client.PrevBufferMsg{})
	if activeID(a) != 3 {
		t.Errorf("previous buffer = %d, want 3", activeID(a))
	}
}

func TestClosingABufferStaysInItsGroupAndAnEmptyGroupGoes(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go", "d.go")
	a = newGroupNamed(t, a, "g") // 1
	a, _ = step(t, a, bufPickedMsg{idx: 2})
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupMove})
	a = confirm(t, a) // g: 1, 3
	a, _ = step(t, a, bufPickedMsg{idx: 0})

	// Closing 1 goes to its group neighbour 3, not to 2 beside it in a.buffers.
	a, _ = step(t, a, client.CloseBufferMsg{})
	if activeID(a) != 3 {
		t.Fatalf("after closing 1, active = %d, want 3", activeID(a))
	}
	a, _ = step(t, a, bufPickedMsg{idx: 0}) // 2, ungrouped
	a, _ = step(t, a, bufPickedMsg{idx: 1}) // 3, back in g
	a, _ = step(t, a, client.CloseBufferMsg{})
	if got := groupsOf(a); got != "ungrouped:2,4" {
		t.Errorf("groups = %q, want g deleted with its last buffer", got)
	}
	if activeID(a) != 2 {
		t.Errorf("active = %d, want 2, the most recently used remaining", activeID(a))
	}
}

func TestMovePickerOffersOtherGroupsAndANewOne(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupMove})
	var labels []string
	for _, it := range a.groupPicker.items {
		labels = append(labels, it.label)
	}
	if !slices.Equal(labels, []string{"ungrouped (2)", newGroupLabel}) {
		t.Fatalf("move picker = %v", labels)
	}
	// "New group…" goes on to the name prompt.
	a, _ = step(t, a, tea.KeyPressMsg{Code: tea.KeyDown})
	a, _ = step(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.pluginInput == nil || a.pluginInput.title != "New Tab Group" {
		t.Fatal("New group… did not open the name prompt")
	}
	a = confirm(t, typeText(t, a, "web"))
	if got := groupsOf(a); got != "ungrouped:2,3 web:1" {
		t.Errorf("groups = %q, want api emptied and deleted", got)
	}
}

func TestNamingANewGroupAfterAnExistingOneMovesIntoIt(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, bufPickedMsg{idx: 1})
	a = newGroupNamed(t, a, "api")
	if got := groupsOf(a); got != "api:1,2" {
		t.Errorf("groups = %q", got)
	}
}

func TestRenameGroup(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupRename})
	if a.pluginInput == nil || a.pluginInput.text != "api" {
		t.Fatal("rename prompt not prefilled with the current name")
	}
	for range 3 {
		a, _ = step(t, a, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	a = confirm(t, typeText(t, a, "server"))
	if got := groupsOf(a); got != "ungrouped:2 server:1" || a.groupName != "server" {
		t.Fatalf("groups = %q, current %q", got, a.groupName)
	}

	// Renaming to another group's name is refused.
	a, _ = step(t, a, bufPickedMsg{idx: 1})
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupRename})
	a = confirm(t, typeText(t, a, "server"))
	if got := groupsOf(a); got != "ungrouped:2 server:1" {
		t.Errorf("groups = %q after a clashing rename", got)
	}
	if !strings.Contains(a.status, "already") {
		t.Errorf("status = %q, want the clash reported", a.status)
	}
}

func TestEmptyGroupNameIsRefused(t *testing.T) {
	a := newGroupNamed(t, newGroupApp("a.go", "b.go"), "  ")
	if got := groupsOf(a); got != "ungrouped:1,2" {
		t.Errorf("groups = %q", got)
	}
	if !strings.HasPrefix(a.status, "E:") {
		t.Errorf("status = %q, want an error", a.status)
	}
}

func TestSwitchingGroupsGoesToItsMostRecentBuffer(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a, _ = step(t, a, bufPickedMsg{idx: 2})
	a, _ = step(t, a, bufPickedMsg{idx: 1}) // ungrouped: 1,2,3, most recent 2 then 3
	a = newGroupNamed(t, a, "api")          // api: 2; ungrouped most recent now 3
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupNext})
	if activeID(a) != 3 {
		t.Errorf("next group landed on %d, want 3, ungrouped's most recent", activeID(a))
	}
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupPrev})
	if activeID(a) != 2 || a.groupName != "api" {
		t.Errorf("previous group: buffer %d in %q, want 2 in api", activeID(a), a.groupName)
	}

	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupSwitch})
	if a.groupPicker == nil || a.groupPicker.items[a.groupPicker.cursor].name != "api" {
		t.Fatal("switch picker does not open on the current group")
	}
	a, _ = step(t, a, tea.KeyPressMsg{Code: tea.KeyUp})
	a, _ = step(t, a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if a.groupName != "" || activeID(a) != 3 {
		t.Errorf("switch picker: group %q buffer %d", a.groupName, activeID(a))
	}
}

func TestCloseGroupClosesOnlyItsBuffersAndRefusesUnsaved(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, bufPickedMsg{idx: 1})
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupMove})
	a = confirm(t, a) // api: 1, 2

	dirty, _ := a.buffers[0].ApplyExternalOps([]document.Op{{Type: document.OpInsert, InsertText: "x"}})
	a.buffers[0] = dirty
	a, _ = step(t, a, client.TabGroupMsg{Action: client.TabGroupClose})
	if len(a.buffers) != 3 || !strings.Contains(a.status, "unsaved") {
		t.Fatalf("closed a group with unsaved changes: %d buffers, status %q", len(a.buffers), a.status)
	}

	a.buffers[0] = newReloadTestModel(1, "/tmp/a.go")
	a, cmd := step(t, a, client.TabGroupMsg{Action: client.TabGroupClose})
	if cmd == nil {
		t.Error("closing a group released nothing on the server")
	}
	if len(a.buffers) != 1 || activeID(a) != 3 || groupsOf(a) != "ungrouped:3" {
		t.Errorf("after closing api: buffers %d, active %d, groups %q", len(a.buffers), activeID(a), groupsOf(a))
	}
}

// TestClickingAGroupNameSwitchesGroups goes through Update, so the click is
// tested where it is dispatched, and checks the click lands on the name
// renderTabBar draws there.
func TestClickingAGroupNameSwitchesGroups(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	bar := ansi.Strip(a.renderTabBar())
	col := strings.Index(bar, "ungrouped")
	if gi, ok := a.groupAtColumn(col); !ok || a.groups[gi].name != "" {
		t.Fatalf("groupAtColumn(%d) = %d, %v", col, gi, ok)
	}
	a, _ = step(t, a, tea.MouseClickMsg{Button: tea.MouseLeft, X: col, Y: 0})
	if a.groupName != "" || activeID(a) != 2 {
		t.Errorf("after the click: group %q buffer %d", a.groupName, activeID(a))
	}
	// A tab after the strip is still hit where it is drawn.
	tabCol := strings.Index(ansi.Strip(a.renderTabBar()), "b.go")
	if idx, ok := a.tabAtColumn(tabCol); !ok || a.buffers[idx].BufID() != 2 {
		t.Errorf("tabAtColumn(%d) = %d, %v", tabCol, idx, ok)
	}
	if _, ok := a.tabAtColumn(0); ok {
		t.Error("a click on the strip resolved to a tab")
	}
}

// TestGroupStripFitsANarrowBar: the names give way, the active one last, and
// the bar never exceeds the terminal.
func TestGroupStripFitsANarrowBar(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a = newGroupNamed(t, a, "frontend-components")
	a, _ = step(t, a, bufPickedMsg{idx: 1})
	a = newGroupNamed(t, a, "backend-services")
	for _, w := range []int{100, 60, 40, 20, 8} {
		a.width = w
		bar := a.renderTabBar()
		if got := lipgloss.Width(bar); got > w {
			t.Errorf("width %d: bar is %d wide", w, got)
		}
		if w >= 40 && !strings.Contains(ansi.Strip(bar), "backend-services") {
			t.Errorf("width %d: the active group's name is missing from %q", w, ansi.Strip(bar))
		}
		if w >= 40 && !strings.Contains(ansi.Strip(bar), "b.go") {
			t.Errorf("width %d: the active tab is missing from %q", w, ansi.Strip(bar))
		}
	}
}

func TestBufferPickerShowsGroups(t *testing.T) {
	a := newGroupApp("a.go", "b.go")
	a = newGroupNamed(t, a, "api")
	a, _ = step(t, a, client.OpenBufPickerMsg{})
	if a.bufPicker == nil {
		t.Fatal("no buffer picker")
	}
	if out := ansi.Strip(a.bufPicker.render()); !strings.Contains(out, "a.go  · api") || !strings.Contains(out, "b.go  · ungrouped") {
		t.Errorf("picker:\n%s", out)
	}
	plain := newGroupApp("a.go", "b.go")
	plain, _ = step(t, plain, client.OpenBufPickerMsg{})
	if strings.Contains(ansi.Strip(plain.bufPicker.render()), "·") {
		t.Error("the picker labels groups when there are none")
	}
}
