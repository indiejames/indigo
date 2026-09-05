package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

func TestDialogResultsW(t *testing.T) {
	cases := []struct {
		termW int
		want  int
	}{
		{200, 188},
		{100, 88},
		{50, 38},
		{10, 24}, // floor
	}
	for _, c := range cases {
		if got := dialogResultsW(c.termW); got != c.want {
			t.Errorf("dialogResultsW(%d) = %d, want %d", c.termW, got, c.want)
		}
	}
}

func TestBoldPreserving(t *testing.T) {
	if got := boldPreserving("MATCH"); got != "\x1b[1mMATCH\x1b[22m" {
		t.Errorf("boldPreserving(%q) = %q, want bold-on/off wrapped, no full reset", "MATCH", got)
	}
	if got := boldPreserving(""); got != "" {
		t.Errorf("boldPreserving(\"\") = %q, want unchanged empty string", got)
	}
}

func TestRenderResultLineBoldsMatch(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 20)
	r := GrepResult{RelPath: "a.go", Line: 0, Col: 4, MatchLen: 5, LineText: "foo MATCH bar"}

	// Non-selected, non-diff: the match is rendered via sraMatchStyle (bold),
	// a normal lipgloss Render (full reset) since nothing behind it needs a
	// background preserved.
	line := d.renderResultLine(r, false)
	if !strings.Contains(line, sraMatchStyle.Render("MATCH")) {
		t.Errorf("expected the match rendered via sraMatchStyle, got %q", line)
	}
	if !strings.Contains(ansi.Strip(line), "foo MATCH bar") {
		t.Errorf("expected match content preserved once ANSI is stripped, got %q (stripped: %q)", line, ansi.Strip(line))
	}

	// Selected row: bold is applied via boldPreserving (no full reset),
	// so it doesn't cut a gap in sraSelStyle's background.
	selLine := d.renderResultLine(r, true)
	if !strings.Contains(selLine, "\x1b[1mMATCH\x1b[22m") {
		t.Errorf("expected boldPreserving-wrapped match in the selected row, got %q", selLine)
	}
}

func TestRenderResultLineKeepsMatchVisibleWhenLineIsLong(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 60, 20) // dialogResultsW(60) = 48

	pad := strings.Repeat("x", 200)
	r := GrepResult{RelPath: "a.go", Line: 0, Col: len(pad), MatchLen: 6, LineText: pad + "NEEDLE" + strings.Repeat("y", 200)}

	line := d.renderResultLine(r, false)
	if !strings.Contains(line, "NEEDLE") {
		t.Errorf("match far into a long line was not kept visible: %q", line)
	}
	if !strings.HasPrefix(line, "a.go:1:") {
		t.Errorf("expected the file:line label to stay intact: %q", line)
	}
	if got := lipgloss.Width(line); got > dialogResultsW(d.width) {
		t.Errorf("rendered line width %d exceeds dialogResultsW(%d)=%d: %q", got, d.width, dialogResultsW(d.width), line)
	}
	// The match sits in the middle of a very long line on both sides, so a
	// best-effort centered window should show context on both sides, not
	// just up to the match. Strip ANSI first: the match is bold-wrapped, so
	// the styled string has an SGR reset sitting between "NEEDLE" and the
	// trailing context that would defeat a literal adjacency check.
	stripped := ansi.Strip(line)
	if !strings.Contains(stripped, "x…NEEDLEy") && !strings.Contains(stripped, "xNEEDLEy") {
		t.Errorf("expected trailing context after the match, got %q (stripped: %q)", line, stripped)
	}
}

func TestSplitContext(t *testing.T) {
	// Both sides longer than their fair share: split ~evenly.
	before, after := splitContext("aaaaaaaaaa", "bbbbbbbbbb", 6)
	if lipgloss.Width(before)+lipgloss.Width(after) > 6 {
		t.Errorf("splitContext exceeded avail=6: %q / %q", before, after)
	}
	if !strings.HasPrefix(before, "…") || !strings.HasSuffix(after, "…") {
		t.Errorf("expected ellipses on the cut sides: %q / %q", before, after)
	}

	// before is short — after should get the leftover budget.
	before, after = splitContext("ab", "bbbbbbbbbb", 6)
	if before != "ab" {
		t.Errorf("short before side should be shown in full, got %q", before)
	}
	if lipgloss.Width(after) != 4 {
		t.Errorf("expected after to receive before's unused budget (4 runes), got %q", after)
	}

	// Both fit: no truncation at all.
	before, after = splitContext("ab", "cd", 100)
	if before != "ab" || after != "cd" {
		t.Errorf("splitContext(%q,%q,100) = %q,%q, want unchanged", "ab", "cd", before, after)
	}
}

func TestFitSideWideRunes(t *testing.T) {
	// Each of these CJK characters is 2 terminal cells wide (runewidth), so
	// a naive rune-count budget would let the result silently render wider
	// than requested.
	s := "中文测试内容更多字符"
	for budget := 0; budget <= 12; budget++ {
		for _, keepEnd := range []bool{false, true} {
			got := fitSide(s, budget, keepEnd)
			if w := lipgloss.Width(got); w > budget {
				t.Errorf("fitSide(%q, %d, %v) = %q with width %d > budget", s, budget, keepEnd, got, w)
			}
		}
	}
}

func TestSplitContextWideRunes(t *testing.T) {
	before := "中文前缀内容"
	after := "后缀更多文字内容"
	for avail := 0; avail <= 20; avail++ {
		b, a := splitContext(before, after, avail)
		if w := lipgloss.Width(b) + lipgloss.Width(a); w > avail {
			t.Errorf("splitContext(avail=%d) total width %d exceeds avail: %q / %q", avail, w, b, a)
		}
	}
}

func TestRenderResultLineWideRunesRespectWidth(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 60, 20)
	r := GrepResult{
		RelPath:  "a.go",
		Line:     0,
		Col:      20,
		MatchLen: 5,
		LineText: strings.Repeat("字", 20) + "MATCH" + strings.Repeat("符", 20),
	}
	d.results = []GrepResult{r}
	d.refreshResultsView()

	line := d.renderResultLine(r, false)
	if w := lipgloss.Width(line); w > d.resultsW() {
		t.Errorf("renderResultLine width %d exceeds resultsW()=%d: %q", w, d.resultsW(), line)
	}
	selLine := d.renderResultLine(r, true)
	if w := lipgloss.Width(selLine); w > d.resultsW() {
		t.Errorf("selected renderResultLine width %d exceeds resultsW()=%d: %q", w, d.resultsW(), selLine)
	}
}

func TestSearchReplaceResizeRefreshesResultsView(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 300, 40)
	d.results = []GrepResult{{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 3, LineText: "foo bar baz"}}
	d.refreshResultsView()
	correctW := d.resultsMaxContentW

	// Corrupt the cached natural-content width the way it'd be stale if a
	// resize only poked viewport.Width (the pre-fix behavior) instead of
	// rerunning refreshResultsView — resultsMaxContentW is a distinguishing
	// side effect only refreshResultsView touches, so this proves the full
	// refresh actually ran rather than relying on bubbles viewport's
	// internal wrap/clip behavior (an implementation detail).
	d.resultsMaxContentW = -999

	a := App{searchReplace: d, cfg: &config.Config{}}
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a2 := updated.(App)

	if a2.searchReplace.resultsMaxContentW != correctW {
		t.Errorf("resultsMaxContentW = %d after resize, want %d recomputed — refreshResultsView must rerun on resize, not just viewport.Width", a2.searchReplace.resultsMaxContentW, correctW)
	}
	if a2.searchReplace.viewport.Width() != a2.searchReplace.resultsW() {
		t.Errorf("viewport.Width = %d, want %d (resultsW() after resize)", a2.searchReplace.viewport.Width(), a2.searchReplace.resultsW())
	}
}

func TestSearchReplaceResultsWShrinksToFitContent(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 200, 40) // dialogInnerW=64, dialogResultsW=188

	// No results yet: the box should sit at the input-dialog's minimum
	// width, not the full terminal-width-based cap.
	if got, want := d.resultsW(), dialogInnerW(d.width); got != want {
		t.Errorf("empty results: resultsW() = %d, want floor %d", got, want)
	}

	// A short result shouldn't force the box wider than that same floor.
	d.results = []GrepResult{{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 2, LineText: "ab"}}
	d.refreshResultsView()
	if got, want := d.resultsW(), dialogInnerW(d.width); got != want {
		t.Errorf("short result: resultsW() = %d, want floor %d", got, want)
	}
	if d.viewport.Width() != d.resultsW() {
		t.Errorf("viewport.Width = %d, want %d to match resultsW()", d.viewport.Width(), d.resultsW())
	}

	// A very long result line should widen the box, but never past
	// dialogResultsW's terminal-width-based cap.
	longLine := strings.Repeat("x", 500)
	d.results = []GrepResult{{RelPath: "b.go", Line: 0, Col: 10, MatchLen: 2, LineText: longLine}}
	d.refreshResultsView()
	if got := d.resultsW(); got != dialogResultsW(d.width) {
		t.Errorf("very long result: resultsW() = %d, want cap %d", got, dialogResultsW(d.width))
	}

	// A moderately long result (natural width between the floor and the
	// cap) should size the box to fit it exactly, not jump straight to
	// the cap.
	d.results = []GrepResult{{RelPath: "c.go", Line: 0, Col: 5, MatchLen: 2, LineText: "0123456789"}}
	natural := d.resultLineNaturalWidth(d.results[0])
	d.refreshResultsView()
	if got := d.resultsW(); got != max(natural, dialogInnerW(d.width)) {
		t.Errorf("moderate result: resultsW() = %d, want max(natural=%d, floor=%d)", got, natural, dialogInnerW(d.width))
	}
}

func TestFocusOrderExpandsWithReplaceAndResults(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)

	order := d.focusOrder()
	want := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusFilterToggle, sraFocusToggle}
	if len(order) != len(want) {
		t.Fatalf("focusOrder() = %v, want %v", order, want)
	}

	d.replaceOpen = true
	d.results = []GrepResult{{RelPath: "a.go"}}
	order = d.focusOrder()
	wantOpen := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusFilterToggle, sraFocusToggle, sraFocusReplace, sraFocusAll, sraFocusResults}
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
	updated, cmd := a.handleSearchReplaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	updated, cmd := a.handleSearchReplaceKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestFocusOrderExpandsWithFilter(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	d.filterOpen = true

	order := d.focusOrder()
	want := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusFilterToggle, sraFocusInclude, sraFocusExclude, sraFocusToggle}
	if len(order) != len(want) {
		t.Fatalf("focusOrder() with filterOpen = %v, want %v", order, want)
	}
	for i, f := range want {
		if order[i] != f {
			t.Errorf("focusOrder()[%d] = %v, want %v", i, order[i], f)
		}
	}
}

func TestSetFocusManagesIncludeExcludeInputFocus(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)

	d.setFocus(sraFocusInclude)
	if !d.includeInput.Focused() {
		t.Error("includeInput not focused after setFocus(Include)")
	}
	if d.excludeInput.Focused() {
		t.Error("excludeInput should not be focused after setFocus(Include)")
	}

	d.setFocus(sraFocusExclude)
	if d.includeInput.Focused() {
		t.Error("includeInput still focused after setFocus(Exclude)")
	}
	if !d.excludeInput.Focused() {
		t.Error("excludeInput not focused after setFocus(Exclude)")
	}

	d.setFocus(sraFocusSearch)
	if d.includeInput.Focused() || d.excludeInput.Focused() {
		t.Error("include/exclude inputs should be blurred after setFocus(Search)")
	}
}

// TestSpaceTogglesFilterOpen is a regression test for the space-bar toggle
// wiring in handleSearchReplaceKey: pressing space on the Filter toggle
// should open the section and, when closing it again, blur both inputs so a
// stray keystroke can't land in a hidden field.
func TestSpaceTogglesFilterOpen(t *testing.T) {
	d := newSearchReplaceDialog("/tmp", 100, 40)
	d.setFocus(sraFocusFilterToggle)
	a := App{searchReplace: d}

	updated, _ := a.handleSearchReplaceKey(tea.KeyPressMsg{Code: tea.KeySpace})
	a2 := updated.(App)
	if !a2.searchReplace.filterOpen {
		t.Fatal("filterOpen should be true after space on Filter toggle")
	}

	updated, _ = a2.handleSearchReplaceKey(tea.KeyPressMsg{Code: tea.KeySpace})
	a3 := updated.(App)
	if a3.searchReplace.filterOpen {
		t.Fatal("filterOpen should be false after a second space on Filter toggle")
	}
	if a3.searchReplace.includeInput.Focused() || a3.searchReplace.excludeInput.Focused() {
		t.Error("include/exclude inputs should be blurred once the filter section closes")
	}
}

// TestStartSearchReplaceSearchUsesIncludeExclude confirms the include/exclude
// input values actually reach searchWorkspaceExplicit, not just the pattern.
func TestStartSearchReplaceSearchUsesIncludeExclude(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"main.go":      "hello\n",
		"main_test.go": "hello\n",
		"readme.md":    "hello\n",
	})
	d := newSearchReplaceDialog(dir, 100, 40)
	d.searchInput.SetValue("hello")
	d.includeInput.SetValue("*.go")
	d.excludeInput.SetValue("*_test.go")

	a := App{searchReplace: d}
	cmd := a.startSearchReplaceSearch(d)
	if cmd == nil {
		t.Fatal("expected a non-nil search command")
	}
	msg := cmd()
	batchMsgs, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", msg)
	}
	var results []GrepResult
	var found bool
	for _, sub := range batchMsgs {
		if m, ok := sub().(sraResultsMsg); ok {
			results = m.results
			found = true
		}
	}
	if !found {
		t.Fatal("no sraResultsMsg found in batch")
	}
	if len(results) != 1 || results[0].RelPath != "main.go" {
		t.Errorf("results = %+v, want only main.go", results)
	}
}
