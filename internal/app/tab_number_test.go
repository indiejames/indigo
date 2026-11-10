package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
)

func tabNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("f%d.go", i+1)
	}
	return names
}

// TestADigitSelectsItsTab: 1 to 8 count from the left, 9 is the last tab, and
// a number past the last tab does nothing.
func TestADigitSelectsItsTab(t *testing.T) {
	a := newGroupApp(tabNames(3)...)
	cases := []struct {
		n      int
		wantID uint32
	}{{2, 2}, {1, 1}, {9, 3}, {3, 3}, {5, 3}}
	for _, c := range cases {
		a, _ = step(t, a, client.GoToTabMsg{N: c.n})
		if got := activeID(a); got != c.wantID {
			t.Errorf("after %d: active buffer %d, want %d", c.n, got, c.wantID)
		}
	}
}

// TestNineIsTheLastTabPastNine: the tabs between the eighth and the last have
// no number, and the last is labelled with the key that reaches it.
func TestNineIsTheLastTabPastNine(t *testing.T) {
	a := newGroupApp(tabNames(12)...)
	a.width = 0 // lay out every tab, none hidden for room
	a, _ = step(t, a, client.GoToTabMsg{N: 9})
	if got := activeID(a); got != 12 {
		t.Errorf("9 selected buffer %d, want 12, the last", got)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 8})
	if got := activeID(a); got != 8 {
		t.Errorf("8 selected buffer %d, want 8", got)
	}
	slots, _ := a.tabLayout()
	want := map[int]string{0: "  1 f1.go  ", 7: "  8 f8.go  ", 8: "  f9.go  ", 10: "  f11.go  ", 11: "  9 f12.go  "}
	for i, label := range want {
		if got := slots[i].label; got != label {
			t.Errorf("tab %d label = %q, want %q", i+1, got, label)
		}
	}
}

// TestTabNumbersCountWithinTheGroup: the numbers are the ones the bar shows,
// which is only the current group's tabs.
func TestTabNumbersCountWithinTheGroup(t *testing.T) {
	a := newGroupApp("a.go", "b.go", "c.go")
	a, _ = step(t, a, switchBufferMsg{idx: 2, line: -1, col: -1})
	a = newGroupNamed(t, a, "api") // c.go alone in api
	if got := ansi.Strip(a.renderTabBar()); !strings.Contains(got, "│  1 c.go  ") {
		t.Errorf("bar = %q, want c.go numbered 1 in its group", got)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 2})
	if got := activeID(a); got != 3 {
		t.Errorf("2 left the group for buffer %d; api has only one tab", got)
	}
	a = a.switchToGroup(0)
	a, _ = step(t, a, client.GoToTabMsg{N: 2})
	if got := activeID(a); got != 2 {
		t.Errorf("2 in ungrouped selected buffer %d, want b.go", got)
	}
}

// TestDigitsWorkWithTheTabBarHidden: hide_tabs hides the numbers, not the keys.
func TestDigitsWorkWithTheTabBarHidden(t *testing.T) {
	a := newGroupApp(tabNames(3)...)
	a.cfg.HideTabs = true
	a, _ = step(t, a, client.GoToTabMsg{N: 3})
	if got := activeID(a); got != 3 {
		t.Errorf("active buffer %d, want 3", got)
	}
}

// narrowTabApp has ten tabs on a bar with room for four: each numbered tab is
// 11 columns ("  1 f1.go  "), and 60 less both overflow markers leaves 48.
func narrowTabApp() App {
	a := newGroupApp(tabNames(10)...)
	a.width = 60
	return a.scrollTabs()
}

func shownLabels(a App) []string {
	slots, _ := a.tabLayout()
	out := make([]string, len(slots))
	for i, s := range slots {
		out[i] = strings.TrimSpace(s.label)
	}
	return out
}

// TestTabNumbersCountTheShownTabs is the report: numbers belong to the bar's
// positions, not to buffers, so only as many show as tabs do, and the
// leftmost is always 1 however far the bar has scrolled.
func TestTabNumbersCountTheShownTabs(t *testing.T) {
	a := narrowTabApp()
	a, _ = step(t, a, client.GoToTabMsg{N: 9}) // the last tab shown
	for range 4 {
		a, _ = step(t, a, client.NextBufferMsg{})
	}
	if got := activeID(a); got != 8 {
		t.Fatalf("test setup: active buffer %d, want 8", got)
	}
	want := []string{"1 f5.go", "2 f6.go", "3 f7.go", "4 f8.go"}
	if got := shownLabels(a); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("shown tabs = %q, want %q", got, want)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 1})
	if got := activeID(a); got != 5 {
		t.Errorf("1 selected buffer %d, want 5, the leftmost shown", got)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 9})
	if got := activeID(a); got != 8 {
		t.Errorf("9 selected buffer %d, want 8, the last shown", got)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 5})
	if got := activeID(a); got != 8 {
		t.Errorf("5 selected buffer %d with four tabs shown, want no change", got)
	}
}

// TestMovingAlongTheTabsScrollsOnlyAtTheEdge is the other half of the report:
// moving onto a tab the bar shows must not change the bar, and moving past
// its edge scrolls by one, so no shown tab is swapped for a different file.
func TestMovingAlongTheTabsScrollsOnlyAtTheEdge(t *testing.T) {
	a := narrowTabApp()
	start := fmt.Sprint(shownTabs(a))
	for want := uint32(2); want <= 4; want++ {
		a, _ = step(t, a, client.NextBufferMsg{})
		if got := fmt.Sprint(shownTabs(a)); got != start {
			t.Fatalf("moving to buffer %d changed the bar from %s to %s", want, start, got)
		}
	}
	a, _ = step(t, a, client.NextBufferMsg{})
	if got, want := fmt.Sprint(shownTabs(a)), "[2 3 4 5]"; got != want {
		t.Errorf("past the right edge the bar shows %s, want %s", got, want)
	}
	for range 3 { // back to buffer 2, the leftmost shown
		a, _ = step(t, a, client.PrevBufferMsg{})
	}
	if got, want := fmt.Sprint(shownTabs(a)), "[2 3 4 5]"; got != want {
		t.Errorf("moving left within the bar changed it to %s, want %s", got, want)
	}
	a, _ = step(t, a, client.PrevBufferMsg{})
	if got, want := fmt.Sprint(shownTabs(a)), "[1 2 3 4]"; got != want {
		t.Errorf("past the left edge the bar shows %s, want %s", got, want)
	}
	// Wrapping from the first tab to the last shows the last four.
	a, _ = step(t, a, client.PrevBufferMsg{})
	if got, want := fmt.Sprint(shownTabs(a)), "[7 8 9 10]"; got != want {
		t.Errorf("after wrapping to the last tab the bar shows %s, want %s", got, want)
	}
}

// TestHiddenTabsAreCountedAtTheirEnd: a scrolled bar marks the tabs hidden on
// each side, and a click on the left marker selects nothing.
func TestHiddenTabsAreCountedAtTheirEnd(t *testing.T) {
	a := narrowTabApp()
	for range 5 {
		a, _ = step(t, a, client.NextBufferMsg{})
	}
	bar := ansi.Strip(a.renderTabBar())
	if !strings.HasPrefix(bar, "  ‹2  ") || !strings.Contains(bar, "  4›  ") {
		t.Errorf("bar = %q, want 2 hidden on the left and 4 on the right", bar)
	}
	if _, ok := a.tabAtColumn(0); ok {
		t.Error("a click on the left marker selected a tab")
	}
	if got, ok := a.tabAtColumn(len("  ‹2  ")); !ok || a.buffers[got].BufID() != 3 {
		t.Errorf("a click just past the left marker selected buffer index %d, want buffer 3", got)
	}
}
