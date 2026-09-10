package app

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// newOverflowApp builds an App with n buffers named a.go, b.go, ... — each
// tab is "  x.go  ", 8 columns wide — at the given terminal width.
func newOverflowApp(n, width int) App {
	buffers := make([]client.Model, n)
	for i := range buffers {
		name := fmt.Sprintf("%c.go", 'a'+i)
		buffers[i] = newReloadTestModel(uint32(i+1), "/tmp/"+name)
	}
	return App{
		buffers: buffers,
		active:  0,
		width:   width,
		height:  24,
		cfg:     &config.Config{},
		tabUse:  map[uint32]uint64{},
	}
}

// visit marks buffer idx as the most recently active one, the way a real
// buffer switch does via Update → stampActiveTab.
func (a *App) visit(idx int) {
	a.active = idx
	*a = a.stampActiveTab()
}

func slotIndices(slots []tabSlot) []int {
	out := make([]int, len(slots))
	for i, s := range slots {
		out[i] = s.idx
	}
	return out
}

func TestTabLayoutShowsEveryTabWhenTheyFit(t *testing.T) {
	a := newOverflowApp(4, 80) // 4 × 8 = 32 columns of 80
	slots, hidden := a.tabLayout()

	if hidden != 0 {
		t.Errorf("hidden = %d, want 0 — everything fits", hidden)
	}
	if got := slotIndices(slots); len(got) != 4 {
		t.Errorf("slots = %v, want all four buffers", got)
	}
}

// TestTabLayoutKeepsMostRecentTabs is the reported bug: with more buffers than
// fit, the bar used to emit all of them and let the terminal cut off whatever
// ran past the edge — so the tabs you were actually using could be the ones
// off-screen. Recency decides who stays.
func TestTabLayoutKeepsMostRecentTabs(t *testing.T) {
	a := newOverflowApp(10, 40) // room for 4 tabs, minus the overflow marker
	for _, idx := range []int{9, 7, 3} {
		a.visit(idx)
	}

	slots, hidden := a.tabLayout()
	got := slotIndices(slots)
	if hidden != len(a.buffers)-len(slots) {
		t.Errorf("hidden = %d, want %d", hidden, len(a.buffers)-len(slots))
	}
	for _, want := range []int{3, 7, 9} {
		if !containsInt(got, want) {
			t.Errorf("tab %d (recently visited) missing from %v", want, got)
		}
	}
	// Tab 5 was never visited and there isn't room for everyone.
	if containsInt(got, 5) && !containsInt(got, 9) {
		t.Errorf("never-visited tab 5 kept while recent tab 9 was dropped: %v", got)
	}
}

// TestTabLayoutRendersInBufferOrder: recency chooses *which* tabs survive, but
// they must stay in buffer order — a bar that reorders itself on every switch
// would move tabs out from under the pointer.
func TestTabLayoutRendersInBufferOrder(t *testing.T) {
	a := newOverflowApp(10, 40)
	for _, idx := range []int{9, 2, 7, 4} {
		a.visit(idx)
	}

	got := slotIndices(mustSlots(t, a))
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("tabs not in buffer order: %v", got)
		}
	}
}

// TestTabLayoutAlwaysShowsActiveTab is the guarantee that makes the bar usable
// at all: whatever else is dropped, you can always see which buffer you're in.
func TestTabLayoutAlwaysShowsActiveTab(t *testing.T) {
	a := newOverflowApp(30, 40)
	for idx := range a.buffers {
		a.visit(idx) // walk through every buffer, ending on the last
	}

	for _, active := range []int{0, 13, 29} {
		a.visit(active)
		if got := slotIndices(mustSlots(t, a)); !containsInt(got, active) {
			t.Errorf("active tab %d missing from the bar: %v", active, got)
		}
	}
}

// TestTabLayoutTruncatesAnOversizedActiveTab covers the degenerate terminal:
// one tab wider than the whole bar is truncated to fit rather than dropped.
func TestTabLayoutTruncatesAnOversizedActiveTab(t *testing.T) {
	a := newOverflowApp(3, 10)
	a.buffers[1] = newReloadTestModel(99, "/tmp/a-very-long-file-name-indeed.go")
	a.visit(1)

	slots, _ := a.tabLayout()
	got := slotIndices(slots)
	if !containsInt(got, 1) {
		t.Fatalf("oversized active tab was dropped: %v", got)
	}
	for _, s := range slots {
		if s.width > a.width {
			t.Errorf("tab %d is %d columns wide on a %d-column bar", s.idx, s.width, a.width)
		}
	}
}

// TestRenderTabBarFitsTerminalWidth is the visible symptom: the rendered row
// must not exceed the terminal, or it wraps and shoves the buffer down a line.
func TestRenderTabBarFitsTerminalWidth(t *testing.T) {
	for _, n := range []int{2, 10, 40} {
		a := newOverflowApp(n, 60)
		a.visit(n - 1)
		if got := lipgloss.Width(a.renderTabBar()); got > 60 {
			t.Errorf("%d buffers: tab bar is %d columns wide, want ≤ 60", n, got)
		}
	}
}

// TestRenderTabBarFitsWithLongStatus is the case that made the marker itself
// the overflow: a status message long enough to leave a budget narrower than
// "  +29  " meant the marker alone pushed the row past the terminal width and
// wrapped it, shoving the buffer down a line.
func TestRenderTabBarFitsWithLongStatus(t *testing.T) {
	for _, statusLen := range []int{20, 34, 36, 38, 40} {
		a := newOverflowApp(30, 40)
		a.status = strings.Repeat("x", statusLen)
		a.visit(17)

		row := a.renderTabBar()
		if got := lipgloss.Width(row); got > a.width {
			t.Errorf("status of %d columns: tab bar is %d wide, want <= %d", statusLen, got, a.width)
		}
		if !strings.Contains(ansi.Strip(row), a.status) {
			t.Errorf("status of %d columns: status text missing from the row", statusLen)
		}
	}
}

// TestRenderTabBarShowsOverflowCount: the hidden tabs have to be accounted
// for on screen, or buffers appear to have vanished.
func TestRenderTabBarShowsOverflowCount(t *testing.T) {
	a := newOverflowApp(12, 40)
	slots, hidden := a.tabLayout()
	if hidden == 0 {
		t.Fatalf("test setup: expected tabs to overflow, %d slots fit", len(slots))
	}

	if want := fmt.Sprintf("+%d", hidden); !strings.Contains(a.renderTabBar(), want) {
		t.Errorf("tab bar does not show %q", want)
	}
}

// TestTabAtColumnMatchesVisibleTabs: clicks are resolved through the same
// layout, so a click can't switch to a buffer other than the one drawn there.
func TestTabAtColumnMatchesVisibleTabs(t *testing.T) {
	a := newOverflowApp(12, 40)
	for _, idx := range []int{11, 8, 5} {
		a.visit(idx)
	}

	col := 0
	for _, want := range mustSlots(t, a) {
		got, ok := a.tabAtColumn(col)
		if !ok || got != want.idx {
			t.Errorf("tabAtColumn(%d) = (%d, %v), want (%d, true)", col, got, ok, want.idx)
		}
		col += want.width
	}
	// The overflow marker isn't a tab: a click there is swallowed, like the
	// fill area always has been.
	if _, ok := a.tabAtColumn(col); ok {
		t.Errorf("tabAtColumn(%d) resolved to a tab, want the overflow marker to swallow it", col)
	}
}

// TestStampActiveTabRecordsCurrentBuffer covers the wiring: App.Update stamps
// recency for every message, so no a.active assignment site has to remember to.
func TestStampActiveTabRecordsCurrentBuffer(t *testing.T) {
	a := newOverflowApp(3, 80)
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a2 := updated.(App)

	if a2.tabUse[a2.buffers[a2.active].BufID()] == 0 {
		t.Error("active buffer has no recency stamp after Update")
	}
}

// TestForgetTabUseDropsClosedBuffer keeps the map from growing for the life of
// a session as buffers come and go.
func TestForgetTabUseDropsClosedBuffer(t *testing.T) {
	a := newOverflowApp(2, 80)
	a.visit(1)
	id := a.buffers[1].BufID()
	if a.tabUse[id] == 0 {
		t.Fatal("test setup: buffer 1 was not stamped")
	}

	a.active = 1
	updated, _ := a.handleCloseBuffer()
	a2 := updated.(App)

	if _, still := a2.tabUse[id]; still {
		t.Error("closed buffer still has a recency entry")
	}
}

func mustSlots(t *testing.T, a App) []tabSlot {
	t.Helper()
	slots, _ := a.tabLayout()
	if len(slots) == 0 {
		t.Fatal("tabLayout returned no slots")
	}
	return slots
}

func containsInt(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
