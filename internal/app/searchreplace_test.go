package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

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

func TestSraResultsAutoFocusesResultsList(t *testing.T) {
	a := App{searchReplace: newSearchReplaceDialog("/tmp", 100, 40)}

	updated, _ := a.Update(sraResultsMsg{results: []GrepResult{
		{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 3, LineText: "foo bar"},
	}})
	a2 := updated.(App)

	if a2.searchReplace.focus != sraFocusResults {
		t.Errorf("focus after non-empty results = %v, want sraFocusResults", a2.searchReplace.focus)
	}
	if a2.searchReplace.searchInput.Focused() {
		t.Error("searchInput should be blurred once focus moves to results")
	}
}

func TestSraResultsNoAutoFocusWhenEmpty(t *testing.T) {
	a := App{searchReplace: newSearchReplaceDialog("/tmp", 100, 40)}

	updated, _ := a.Update(sraResultsMsg{results: nil})
	a2 := updated.(App)

	if a2.searchReplace.focus != sraFocusSearch {
		t.Errorf("focus after empty results = %v, want sraFocusSearch", a2.searchReplace.focus)
	}
}

// TestSearchReplaceEnterOpensMatchWhenReplaceClosed reproduces the bug where
// selecting a result and pressing Enter in plain-search mode (replace field
// not toggled open) deleted the matched text — acceptSearchReplaceMatch
// always replaced with d.replaceInput.Value(), which is empty when the
// replace field has never been shown. Enter on a result should just open
// the file, like a search, unless replace is toggled open.
func TestSearchReplaceEnterOpensMatchWhenReplaceClosed(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	d.results = []GrepResult{{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 3, LineText: "foo bar"}}
	d.setFocus(sraFocusResults)
	d.cursor = 0
	// d.replaceOpen is false by default.

	a := App{searchReplace: d}
	updated, cmd := a.handleSearchReplaceKey(tea.KeyMsg{Type: tea.KeyEnter})
	a2 := updated.(App)

	if a2.searchReplace != nil {
		t.Error("dialog should close immediately when opening a search result (no replace pending)")
	}
	if cmd == nil {
		t.Error("expected a command to open the matched file")
	}
}

// TestSearchReplaceEnterKeepsDialogOpenWhenReplaceOpen checks that toggling
// the replace field open preserves the existing replace-on-Enter behavior
// (the dialog itself closes later, once the async apply/open completes).
func TestSearchReplaceEnterKeepsDialogOpenWhenReplaceOpen(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	d.results = []GrepResult{{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 3, LineText: "foo bar"}}
	d.replaceOpen = true
	d.setFocus(sraFocusResults)
	d.cursor = 0

	a := App{searchReplace: d}
	updated, cmd := a.handleSearchReplaceKey(tea.KeyMsg{Type: tea.KeyEnter})
	a2 := updated.(App)

	if a2.searchReplace == nil {
		t.Error("dialog should not close synchronously on the replace path")
	}
	if cmd == nil {
		t.Error("expected a command to apply the replacement")
	}
}

func TestOpenSearchReplaceMatchOutOfBounds(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	a := App{searchReplace: d}
	if cmd := a.openSearchReplaceMatch(d); cmd != nil {
		t.Error("expected nil command when there are no results")
	}
}

func newSraTestModel(absPath string) client.Model {
	return client.New(&client.RPC{}, 1, "", 0, absPath, "/tmp", nil, false, 0)
}

// TestSraSingleResultAppliedOutOfBoundsDoesNotPanic reproduces closing a tab
// while a search-and-replace "accept match" RPC is in flight: by the time
// sraSingleResultMsg arrives, am.idx no longer exists in a.buffers. The
// handler must not panic indexing a.buffers[am.idx], and must still return
// am.cmd so the already-applied edit still gets sent to the server.
func TestSraSingleResultAppliedOutOfBoundsDoesNotPanic(t *testing.T) {
	a := App{
		searchReplace: newSearchReplaceDialog("/tmp", 100, 40),
		buffers:       []client.Model{newSraTestModel("/tmp/a.go")},
		active:        0,
	}
	sentinelCmd := func() tea.Msg { return nil }
	msg := sraSingleResultMsg{applied: &bufferAppliedMsg{
		idx:   5, // out of range for a single-buffer App
		model: newSraTestModel("/tmp/b.go"),
		cmd:   sentinelCmd,
		line:  0, col: 0, matchLen: 3,
	}}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if len(a2.buffers) != 1 || a2.buffers[0].FilePath() != "/tmp/a.go" {
		t.Errorf("existing buffer was mutated on a stale/out-of-range idx: %+v", a2.buffers)
	}
	if cmd == nil {
		t.Error("expected am.cmd to still be returned so the edit reaches the server")
	}
}

// TestSraSingleResultAppliedStaleIndexReusedByAnotherBuffer covers the
// subtler case: am.idx is still in range, but a different tab closed in the
// meantime and the slice shifted, so that index now names a different file.
// Applying blindly would silently overwrite the wrong tab.
func TestSraSingleResultAppliedStaleIndexReusedByAnotherBuffer(t *testing.T) {
	a := App{
		searchReplace: newSearchReplaceDialog("/tmp", 100, 40),
		buffers:       []client.Model{newSraTestModel("/tmp/other.go")},
		active:        0,
	}
	msg := sraSingleResultMsg{applied: &bufferAppliedMsg{
		idx:   0,
		model: newSraTestModel("/tmp/a.go"), // no longer matches buffers[0]
		cmd:   func() tea.Msg { return nil },
		line:  0, col: 0, matchLen: 3,
	}}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if a2.buffers[0].FilePath() != "/tmp/other.go" {
		t.Errorf("buffers[0] = %q, want unchanged %q (stale idx reused by a different buffer)", a2.buffers[0].FilePath(), "/tmp/other.go")
	}
	if cmd == nil {
		t.Error("expected am.cmd to still be returned so the edit reaches the server")
	}
}

// TestSraSingleResultAppliedValidIndex is the happy path: idx still points
// at the same buffer, so the model/active/cursor should update normally.
func TestSraSingleResultAppliedValidIndex(t *testing.T) {
	a := App{
		searchReplace: newSearchReplaceDialog("/tmp", 100, 40),
		buffers:       []client.Model{newSraTestModel("/tmp/a.go")},
		active:        0,
		cfg:           &config.Config{},
	}
	newModel := newSraTestModel("/tmp/a.go")
	msg := sraSingleResultMsg{applied: &bufferAppliedMsg{
		idx:   0,
		model: newModel,
		cmd:   func() tea.Msg { return nil },
		line:  0, col: 0, matchLen: 3,
	}}

	updated, cmd := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 0 {
		t.Errorf("active = %d, want 0", a2.active)
	}
	if cmd == nil {
		t.Error("expected a non-nil command on the happy path")
	}
}
