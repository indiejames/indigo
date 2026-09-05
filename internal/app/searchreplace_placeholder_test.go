package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// TestSearchReplaceInputPlaceholdersRenderInFull is a regression test for a
// Bubble Tea v2 upgrade bug that reached a live session: opening the search
// & replace dialog (space s) showed "S" in the search box instead of the
// full "Search" placeholder.
//
// Cause: bubbles v2's textinput.placeholderView copies the placeholder into
// a make([]rune, Width()+1) buffer, so it truncates to Width()+1 runes. At
// the zero-value width that's one rune. bubbles v1 instead split the
// placeholder into first-grapheme + rest and rendered the rest in full when
// no width was set, so these inputs had never needed an explicit width.
//
// The assertion is on the rendered dialog rather than on the width setter,
// so it fails for the symptom the user actually saw regardless of how the
// widths get applied.
func TestSearchReplaceInputPlaceholdersRenderInFull(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 120, 40)
	d.replaceOpen = true
	d.filterOpen = true

	view := ansi.Strip(d.render())

	for _, want := range []string{"Search", "Replace", "Include", "Exclude"} {
		if !strings.Contains(view, want) {
			t.Errorf("placeholder %q missing from the rendered dialog — "+
				"a truncated placeholder renders as just its first character.\n%s",
				want, view)
		}
	}
}

// TestSearchReplaceInputsFitTheirBorder guards the other half of the width
// convention: v2's textinput renders Width()+1 columns (the cursor sits one
// column past the text capacity), so sizing an input to the full inner width
// overflows the surrounding border by one column and wraps it onto an extra
// line. Sizing to innerW-1 is what keeps the box one row tall.
func TestSearchReplaceInputsFitTheirBorder(t *testing.T) {
	for _, termW := range []int{80, 120, 200, 40} {
		d := newSearchReplaceDialog("/tmp", termW, 40)
		innerW := dialogInnerW(termW)
		if got := lipgloss.Width(d.searchInput.View()); got > innerW {
			t.Errorf("termW=%d: search input renders %d columns, exceeding its box's inner width %d — "+
				"it will wrap inside the border", termW, got, innerW)
		}
	}
}

// TestSearchReplaceResizeKeepsPlaceholders pins the resize path: the inputs
// are sized from d.width, so a terminal resize that updates d.width without
// re-sizing them would leave them at the old width (and, growing from a
// narrow start, visibly clipped).
func TestSearchReplaceResizeKeepsPlaceholders(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 40, 20)
	before := d.searchInput.Width()

	d.width = 200
	d.resizeInputs()

	if after := d.searchInput.Width(); after <= before {
		t.Errorf("input width after growing the terminal = %d, want > %d: resizeInputs didn't take effect", after, before)
	}
	if view := ansi.Strip(d.render()); !strings.Contains(view, "Search") {
		t.Errorf("placeholder lost after resize:\n%s", view)
	}
}
