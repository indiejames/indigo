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
	a, _ = step(t, a, client.GoToTabMsg{N: 9})
	if got := activeID(a); got != 12 {
		t.Errorf("9 selected buffer %d, want 12, the last", got)
	}
	a, _ = step(t, a, client.GoToTabMsg{N: 8})
	if got := activeID(a); got != 8 {
		t.Errorf("8 selected buffer %d, want 8", got)
	}
	want := map[int]string{0: "  1 f1.go  ", 7: "  8 f8.go  ", 8: "  f9.go  ", 10: "  f11.go  ", 11: "  9 f12.go  "}
	for i, label := range want {
		if got, _ := a.tabLabel(i); got != label {
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
