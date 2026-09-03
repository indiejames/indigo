package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func newTabClickTestApp() App {
	return App{
		buffers: []client.Model{
			newReloadTestModel(1, "/tmp/a.go"),
			newReloadTestModel(2, "/tmp/b.go"),
			newReloadTestModel(3, "/tmp/c.go"),
		},
		active: 0,
		width:  80,
		height: 24,
		cfg:    &config.Config{},
	}
}

// TestTabAtColumn verifies column-to-tab-index mapping matches the labels
// renderTabBar actually produces ("  name  ", 2-space padding each side,
// no dirty mark on a clean buffer).
func TestTabAtColumn(t *testing.T) {
	a := newTabClickTestApp()

	// "  a.go  " is 8 columns wide; three identical-width tabs in a row.
	_, w := a.tabLabel(0)
	if w != 8 {
		t.Fatalf("tabLabel(0) width = %d, want 8 (test assumes this to compute expected boundaries)", w)
	}

	cases := []struct {
		x       int
		want    int
		wantOK  bool
		comment string
	}{
		{0, 0, true, "first column of tab 0"},
		{7, 0, true, "last column of tab 0"},
		{8, 1, true, "first column of tab 1"},
		{15, 1, true, "last column of tab 1"},
		{16, 2, true, "first column of tab 2"},
		{23, 2, true, "last column of tab 2"},
		{24, -1, false, "past the last tab, in the fill area"},
		{79, -1, false, "far right edge, in the fill area"},
	}
	for _, c := range cases {
		idx, ok := a.tabAtColumn(c.x)
		if ok != c.wantOK || (ok && idx != c.want) {
			t.Errorf("tabAtColumn(%d) [%s] = (%d, %v), want (%d, %v)", c.x, c.comment, idx, ok, c.want, c.wantOK)
		}
	}
}

// TestMouseClickOnTabSwitchesActiveBuffer verifies a left-click landing on
// a tab in the tab bar (row 0) switches to that buffer.
func TestMouseClickOnTabSwitchesActiveBuffer(t *testing.T) {
	a := newTabClickTestApp()

	// Column 8 is the first column of tab 1 (see TestTabAtColumn).
	msg := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 8, Y: 0}
	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 1 {
		t.Errorf("active = %d, want 1 (clicked tab 1)", a2.active)
	}
	if cmd == nil {
		t.Error("expected a non-nil command (ReportActiveContextCmd) after switching tabs")
	}
}

// TestMouseClickOnActiveTabIsNoOp verifies clicking the already-active tab
// doesn't do anything surprising (no crash, no status change).
func TestMouseClickOnActiveTabIsNoOp(t *testing.T) {
	a := newTabClickTestApp()
	a.status = "some status"

	msg := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 0}
	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 0 {
		t.Errorf("active = %d, want 0 (unchanged)", a2.active)
	}
	if a2.status != "some status" {
		t.Errorf("status = %q, want unchanged", a2.status)
	}
}

// TestMouseClickPastLastTabIsSwallowed verifies clicking the tab bar's
// fill/status area (past the last tab) doesn't switch buffers or forward
// the click to the active buffer.
func TestMouseClickPastLastTabIsSwallowed(t *testing.T) {
	a := newTabClickTestApp()

	msg := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 79, Y: 0}
	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 0 {
		t.Errorf("active = %d, want 0 (unchanged; click was past the last tab)", a2.active)
	}
}

// TestMouseWheelOnTabRowStillScrollsActiveBuffer verifies the pre-existing
// behavior — wheel events on the tab bar row still reach the active
// buffer — isn't broken by adding click-to-switch.
func TestMouseWheelOnTabRowStillScrollsActiveBuffer(t *testing.T) {
	a := newTabClickTestApp()

	msg := tea.MouseMsg{Button: tea.MouseButtonWheelDown, X: 0, Y: 0}
	// Must not panic and must still route to the active buffer's Update
	// (Y shifted to -1 → 0 buffer-relative, same as before this change).
	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 0 {
		t.Errorf("active = %d, want 0 (wheel events don't switch tabs)", a2.active)
	}
}
