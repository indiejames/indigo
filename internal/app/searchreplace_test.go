package app

import "testing"

func TestOldTextOf(t *testing.T) {
	cases := []struct {
		name string
		r    GrepResult
		want string
	}{
		{"basic", GrepResult{LineText: "foo bar baz", Col: 4, MatchLen: 3}, "bar"},
		{"start", GrepResult{LineText: "hello world", Col: 0, MatchLen: 5}, "hello"},
		{"unicode", GrepResult{LineText: "héllo wörld", Col: 6, MatchLen: 5}, "wörld"},
		{"matchLenPastEnd", GrepResult{LineText: "abc", Col: 1, MatchLen: 10}, "bc"},
		{"negativeCol", GrepResult{LineText: "abc", Col: -1, MatchLen: 2}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := oldTextOf(c.r); got != c.want {
				t.Errorf("oldTextOf(%+v) = %q, want %q", c.r, got, c.want)
			}
		})
	}
}

func TestDialogInnerW(t *testing.T) {
	cases := []struct {
		termW int
		want  int
	}{
		{200, 64},
		{100, 64},
		{50, 38},
		{10, 24}, // floor
	}
	for _, c := range cases {
		if got := dialogInnerW(c.termW); got != c.want {
			t.Errorf("dialogInnerW(%d) = %d, want %d", c.termW, got, c.want)
		}
	}
}

func TestFocusOrderExpandsWithReplaceAndResults(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)

	order := d.focusOrder()
	want := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusToggle}
	if len(order) != len(want) {
		t.Fatalf("focusOrder() = %v, want %v", order, want)
	}

	d.replaceOpen = true
	d.results = []GrepResult{{RelPath: "a.go"}}
	order = d.focusOrder()
	wantOpen := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusToggle, sraFocusReplace, sraFocusAll, sraFocusResults}
	if len(order) != len(wantOpen) {
		t.Fatalf("focusOrder() with replace+results = %v, want %v", order, wantOpen)
	}
	for i, f := range wantOpen {
		if order[i] != f {
			t.Errorf("focusOrder()[%d] = %v, want %v", i, order[i], f)
		}
	}
}

func TestAdvanceFocusWrapsAndSkipsCollapsedControls(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	// replaceOpen and results are both false/empty, so the order is just
	// search, case, regex, toggle — advancing must never land on Replace/All/Results.
	for i := 0; i < 8; i++ {
		d.advanceFocus(1)
		if d.focus == sraFocusReplace || d.focus == sraFocusAll || d.focus == sraFocusResults {
			t.Fatalf("advanceFocus landed on collapsed control %v at step %d", d.focus, i)
		}
	}

	// Wrapping backward from Search should land on the last entry (Toggle).
	d.setFocus(sraFocusSearch)
	d.advanceFocus(-1)
	if d.focus != sraFocusToggle {
		t.Errorf("advanceFocus(-1) from Search = %v, want Toggle", d.focus)
	}
}

func TestSetFocusManagesTextInputFocus(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)

	d.setFocus(sraFocusReplace)
	if d.searchInput.Focused() {
		t.Error("searchInput still focused after setFocus(Replace)")
	}
	if !d.replaceInput.Focused() {
		t.Error("replaceInput not focused after setFocus(Replace)")
	}

	d.setFocus(sraFocusSearch)
	if !d.searchInput.Focused() {
		t.Error("searchInput not focused after setFocus(Search)")
	}
	if d.replaceInput.Focused() {
		t.Error("replaceInput still focused after setFocus(Search)")
	}
}
