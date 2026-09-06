package client

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/highlight"

	"github.com/indiejames/indigo/internal/config"
)

func virtTestModel(t *testing.T, content string, virt map[int][]virtualLine) Model {
	t.Helper()
	m := newTestModel(content)
	m.width, m.height = 40, 8
	// LineNumbers on: a zero-value Config has it false, but the real default
	// is true, and the gutter layout under test only exists when it's on.
	m.cfg = &config.Config{LineNumbers: true}
	return m.WithVirtualLines(virt)
}

func removed(texts ...string) []virtualLine {
	out := make([]virtualLine, 0, len(texts))
	for _, s := range texts {
		out = append(out, virtualLine{text: s, kind: virtualLineRemoved})
	}
	return out
}

// TestLayoutPlacesVirtualRowsAboveAnchor pins the basic contract: a virtual
// row occupies a screen row of its own, immediately above the buffer line it
// is anchored to, and pushes that line (and everything after) down.
func TestLayoutPlacesVirtualRowsAboveAnchor(t *testing.T) {
	m := virtTestModel(t, "a\nb\nc\n", map[int][]virtualLine{
		1: removed("old b1", "old b2"),
	})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	// Expected rows: a(0), virt, virt, b(1), c(2), ...
	if layout[0].isVirtual || layout[0].bufLine != 0 {
		t.Fatalf("row 0 = %+v, want real buffer line 0", layout[0])
	}
	for i := 1; i <= 2; i++ {
		if !layout[i].isVirtual || layout[i].bufLine != 1 || layout[i].virtualIdx != i-1 {
			t.Errorf("row %d = %+v, want virtual row %d anchored to line 1", i, layout[i], i-1)
		}
	}
	if layout[3].isVirtual || layout[3].bufLine != 1 {
		t.Errorf("row 3 = %+v, want real buffer line 1 below its virtual rows", layout[3])
	}
	if layout[4].isVirtual || layout[4].bufLine != 2 {
		t.Errorf("row 4 = %+v, want real buffer line 2", layout[4])
	}
}

// TestScreenRowOfSkipsVirtualRows is a regression guard for the subtlest bug
// in this design: a virtual row carries its anchor's bufLine and chunk 0, so
// a naive scan would return the virtual row as the position of chunk 0 of
// the anchor line — putting the cursor one row too high, on a row that isn't
// an addressable buffer position at all.
func TestScreenRowOfSkipsVirtualRows(t *testing.T) {
	m := virtTestModel(t, "a\nb\nc\n", map[int][]virtualLine{
		1: removed("old b"),
	})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	// Line 1 is at row 2 (a, virt, b), not row 1 (the virtual row).
	if got := screenRowOf(layout, 1, 0, cw); got != 2 {
		t.Errorf("screenRowOf(line 1) = %d, want 2 — a virtual row shadowed the real line", got)
	}
}

// TestCursorRowAccountsForVirtualRows checks the cursor's screen position
// counts virtual rows above it. Getting this wrong puts the terminal cursor
// somewhere other than where typing lands.
func TestCursorRowAccountsForVirtualRows(t *testing.T) {
	cases := []struct {
		name    string
		virt    map[int][]virtualLine
		line    int
		wantRow int
	}{
		{"no virtual rows", nil, 2, 2},
		{"two above the cursor's line", map[int][]virtualLine{1: removed("x", "y")}, 2, 4},
		{"anchored on the cursor's own line", map[int][]virtualLine{2: removed("x")}, 2, 3},
		{"anchored below the cursor", map[int][]virtualLine{3: removed("x")}, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := virtTestModel(t, "a\nb\nc\nd\n", tc.virt)
			m.cursor.Line = tc.line
			if got := m.cursorVisualRowFromTop(m.contentWidth()); got != tc.wantRow {
				t.Errorf("cursorVisualRowFromTop = %d, want %d", got, tc.wantRow)
			}
		})
	}
}

// TestCursorPositionAgreesWithLayout is the cross-check that matters: the row
// the cursor reports and the row the layout assigns to the cursor's line must
// be the same. They are computed by different code paths
// (cursorVisualRowFromTop vs buildScreenLayout/screenRowOf), so virtual rows
// are exactly the kind of change that can silently desync them.
func TestCursorPositionAgreesWithLayout(t *testing.T) {
	m := virtTestModel(t, "a\nb\nc\nd\n", map[int][]virtualLine{
		1: removed("x", "y"),
		3: removed("z"),
	})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	for line := range 4 {
		m.cursor.Line = line
		fromLayout := screenRowOf(layout, line, 0, cw)
		fromCursor := m.cursorVisualRowFromTop(cw)
		if fromLayout != fromCursor {
			t.Errorf("line %d: layout says row %d, cursorVisualRowFromTop says %d",
				line, fromLayout, fromCursor)
		}
	}
}

// TestVirtualRowRendersTextWithoutLineNumber covers the render branch: the
// supplied text appears, and the gutter stays blank because a virtual row has
// no line number of its own.
func TestVirtualRowRendersTextWithoutLineNumber(t *testing.T) {
	m := virtTestModel(t, "a\nb\n", map[int][]virtualLine{
		1: removed("return err"),
	})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	row := ansi.Strip(m.renderLineChunk(layout[1], cw, nil, -1, -1, false))
	if !strings.Contains(row, "return err") {
		t.Errorf("virtual row = %q, want it to contain the removed text", row)
	}
	if gutterW := m.gutterWidth(); gutterW > 0 && strings.TrimSpace(row[:gutterW]) != "" {
		t.Errorf("virtual row gutter = %q, want blank (no line number)", row[:gutterW])
	}
}

// TestVirtualRowToleratesShrinkingSet covers the debounce race: the git diff
// that feeds the virtual set is recomputed asynchronously, so the set can
// shrink between the layout being built and a row being rendered. That must
// render blank, not panic on an out-of-range index.
func TestVirtualRowToleratesShrinkingSet(t *testing.T) {
	m := virtTestModel(t, "a\nb\n", map[int][]virtualLine{1: removed("x", "y")})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	shrunk := m.WithVirtualLines(map[int][]virtualLine{1: removed("x")})
	// Row 2 was the second virtual row, which no longer exists.
	_ = shrunk.renderLineChunk(layout[2], cw, nil, -1, -1, false) // must not panic
}

// TestNoVirtualLinesIsUnchanged pins that the common case — no diff, no
// virtual rows — produces exactly the layout it did before this feature.
func TestNoVirtualLinesIsUnchanged(t *testing.T) {
	m := virtTestModel(t, "a\nb\nc\n", nil)
	cw := m.contentWidth()
	for i, e := range m.buildScreenLayout(m.visibleLines(), cw) {
		if e.isVirtual {
			t.Errorf("row %d is virtual with no virtual lines set", i)
		}
		if i < 3 && e.bufLine != i {
			t.Errorf("row %d maps to buffer line %d, want %d", i, e.bufLine, i)
		}
	}
}

// TestTintLayersUnderSyntaxHighlighting is the core property of the tint
// design: a diff tint must sit *behind* syntax colours, not replace them.
// Expressing tints as highlight.Spans would have replaced them, because
// spanIdxAt resolves "first span covering this column wins" — this test is
// what pins the separate-layer choice.
func TestTintLayersUnderSyntaxHighlighting(t *testing.T) {
	const fg = "\x1b[38;2;1;2;3m"
	bg := bgSGR("#1E3A1E")
	if bg == "" {
		t.Fatal("bgSGR rejected a valid colour")
	}

	var sb strings.Builder
	renderLineRunes(&sb, []rune("abc"), -1, -1, -1,
		[]highlight.Span{{StartCol: 0, EndCol: 3, ANSI: fg}}, nil, nil,
		[]tintRange{{StartCol: 0, EndCol: 3, BG: bg}})
	got := sb.String()

	if !strings.Contains(got, bg) {
		t.Errorf("rendered = %q, want it to contain the tint background %q", got, bg)
	}
	if !strings.Contains(got, fg) {
		t.Errorf("rendered = %q: the tint replaced the syntax foreground %q instead of "+
			"layering behind it", got, fg)
	}
	if i, j := strings.Index(got, bg), strings.Index(got, fg); i > j {
		t.Errorf("background emitted after foreground in %q; background must come first "+
			"so the foreground paints over it", got)
	}
	if plain := ansi.Strip(got); plain != "abc" {
		t.Errorf("stripped = %q, want %q — no characters lost", plain, "abc")
	}
}

// TestNarrowerTintWinsForIntraLineEmphasis covers the layering rule that
// makes intra-line diffs work: a whole-line tint with a narrow emphasis tint
// inside it must show the emphasis over the middle, not be shadowed by the
// broader range.
func TestNarrowerTintWinsForIntraLineEmphasis(t *testing.T) {
	base, emph := bgSGR("#1E3A1E"), bgSGR("#2F7A2F")

	var sb strings.Builder
	renderLineRunes(&sb, []rune("abcde"), -1, -1, -1, nil, nil, nil, []tintRange{
		{StartCol: 0, EndCol: 5, BG: base},
		{StartCol: 2, EndCol: 3, BG: emph}, // just "c"
	})
	got := sb.String()

	if !strings.Contains(got, emph+"c") {
		t.Errorf("rendered = %q, want the emphasis tint applied to \"c\"", got)
	}
	if !strings.Contains(got, base) {
		t.Errorf("rendered = %q, want the surrounding base tint still present", got)
	}
	if plain := ansi.Strip(got); plain != "abcde" {
		t.Errorf("stripped = %q, want %q", plain, "abcde")
	}
}

// TestSelectionAndCursorWinOverTint pins that transient user state stays
// legible: a tinted line under selection or the cursor must render with the
// selection/cursor styling, not the diff colour.
func TestSelectionAndCursorWinOverTint(t *testing.T) {
	bg := bgSGR("#1E3A1E")
	tints := []tintRange{{StartCol: 0, EndCol: 3, BG: bg}}

	var sel strings.Builder
	renderLineRunes(&sel, []rune("abc"), 0, 2, -1, nil, nil, nil, tints)
	if got := sel.String(); !strings.Contains(got, selectionStyle.Render("abc")) {
		t.Errorf("selected tinted line = %q, want the selection style to win", got)
	}

	var cur strings.Builder
	renderLineRunes(&cur, []rune("abc"), -1, -1, 0, nil, nil, nil, tints)
	if got := cur.String(); !strings.Contains(got, cursorStyle.Render("a")) {
		t.Errorf("cursor on tinted line = %q, want the cursor style to win", got)
	}
}

// TestBgSGRRejectsMalformedColours guards the degradation path: a bad theme
// value must produce no tint rather than emit a broken escape sequence into
// the frame.
func TestBgSGRRejectsMalformedColours(t *testing.T) {
	for _, bad := range []string{"", "#12345", "#1234567", "1E3A1E", "#GGGGGG", "#1e3a1"} {
		if got := bgSGR(bad); got != "" {
			t.Errorf("bgSGR(%q) = %q, want \"\"", bad, got)
		}
	}
	if got, want := bgSGR("#1E3A1E"), "\x1b[48;2;30;58;30m"; got != want {
		t.Errorf("bgSGR(#1E3A1E) = %q, want %q", got, want)
	}
}

// TestWholeLineTintExtendsThroughPadding covers ToEOL: an added or changed
// line should read as a solid bar to the window edge, not stop ragged at
// end-of-text.
func TestWholeLineTintExtendsThroughPadding(t *testing.T) {
	bg := bgSGR("#1E3A1E")
	m := virtTestModel(t, "ab\ncd\n", nil)
	m = m.WithLineTints(map[int][]tintRange{
		0: {{StartCol: 0, EndCol: 2, BG: bg, ToEOL: true}},
	})
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)

	withEOL := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)
	if !strings.Contains(withEOL, bg) {
		t.Fatalf("tinted row lacks the background entirely: %q", withEOL)
	}
	// The untinted row below is the control: same width, no background.
	plainRow := m.renderLineChunk(layout[1], cw, nil, -1, -1, false)
	if strings.Contains(plainRow, bg) {
		t.Errorf("untinted row picked up a background: %q", plainRow)
	}
	if strings.Count(withEOL, bg) < 2 {
		t.Errorf("ToEOL tint appears only once in %q; the trailing padding should carry "+
			"the background too", withEOL)
	}
}

// TestDecorationsBecomeVirtualRowsAndTints covers the transport seam: the
// plugin ships removed lines and tints as decorations, and the client has to
// turn them into layout/render state. Both halves were built separately, so
// this is the test that proves they meet.
func TestDecorationsBecomeVirtualRowsAndTints(t *testing.T) {
	m := virtTestModel(t, "kept\nreturn nil\n", nil)
	m.decorations = []ClientDecoration{
		// Decoration lines are 0-based, like buffer lines.
		{Kind: ClientDecorationRemovedLine, Line: 1, Text: "return err", Col: 7, EndCol: 10},
		{Kind: ClientDecorationLineTint, Line: 1, Col: 0, EndCol: ^uint32(0), TextColor: "#152A40"},
		{Kind: ClientDecorationLineTint, Line: 1, Col: 7, EndCol: 10, TextColor: "#255081"},
	}
	m = m.rebuildDiffDecorations()

	virt := m.virtualLinesBefore(1)
	if len(virt) != 1 || virt[0].text != "return err" {
		t.Fatalf("virtual rows for line 1 = %+v, want the removed line", virt)
	}
	if virt[0].emphStart != 7 || virt[0].emphEnd != 10 {
		t.Errorf("emphasis range = [%d,%d), want [7,10)", virt[0].emphStart, virt[0].emphEnd)
	}

	tints := m.lineTintsFor(1)
	if len(tints) != 2 {
		t.Fatalf("tints for line 1 = %+v, want 2 (whole line + intra-line)", tints)
	}
	if !tints[0].ToEOL {
		t.Error("whole-line tint should set ToEOL so the row reads as a solid bar")
	}
	if tints[1].ToEOL {
		t.Error("intra-line tint must not set ToEOL; it stops at the runes it marks")
	}

	// And it reaches the frame: the removed line renders as its own row.
	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	if !layout[1].isVirtual {
		t.Fatalf("row 1 = %+v, want the virtual removed row", layout[1])
	}
	if got := ansi.Strip(m.renderLineChunk(layout[1], cw, nil, -1, -1, false)); !strings.Contains(got, "return err") {
		t.Errorf("rendered virtual row = %q, want the removed text", got)
	}
}

// TestDiffDecorationsDroppedWhenOutOfRange covers the shrinking-buffer case:
// the diff is recomputed on a debounce, so decorations can outlive the lines
// they point at. They must be dropped, not clamped onto the last line.
func TestDiffDecorationsDroppedWhenOutOfRange(t *testing.T) {
	m := virtTestModel(t, "only\n", nil)
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationRemovedLine, Line: 99, Text: "gone"},
		{Kind: ClientDecorationLineTint, Line: 99, Col: 0, EndCol: 4, TextColor: "#152A40"},
	}
	m = m.rebuildDiffDecorations()

	if len(m.virtualLines) != 0 {
		t.Errorf("virtual rows = %v, want none for an out-of-range line", m.virtualLines)
	}
	if len(m.lineTints) != 0 {
		t.Errorf("tints = %v, want none for an out-of-range line", m.lineTints)
	}
}

// TestMalformedTintColourProducesNoTint pins the degradation path end to end:
// a bad colour from a plugin must yield no tint rather than a broken escape
// in the frame.
func TestMalformedTintColourProducesNoTint(t *testing.T) {
	m := virtTestModel(t, "abc\n", nil)
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationLineTint, Line: 0, Col: 0, EndCol: 3, TextColor: "not-a-colour"},
	}
	m = m.rebuildDiffDecorations()

	if got := m.lineTintsFor(0); len(got) != 0 {
		t.Errorf("tints = %+v, want none for a malformed colour", got)
	}
}

// TestDecorationsMsgRebuildsDiffState covers the wiring itself, not just the
// derivation: a decorations refresh arriving through Update must rebuild the
// virtual-row and tint sets. The sibling test above calls
// rebuildDiffDecorations directly, so it would keep passing if the call were
// dropped from the Update handler — this one wouldn't.
func TestDecorationsMsgRebuildsDiffState(t *testing.T) {
	m := virtTestModel(t, "kept\nreturn nil\n", nil)
	m.bufID = 7

	got, _ := m.Update(decorationsMsg{bufID: 7, items: []ClientDecoration{
		{Kind: ClientDecorationRemovedLine, Line: 1, Text: "return err"},
	}})
	mm := got.(Model)

	if virt := mm.virtualLinesBefore(1); len(virt) != 1 || virt[0].text != "return err" {
		t.Errorf("virtual rows after decorationsMsg = %+v, want the removed line — "+
			"the Update handler is not rebuilding diff state", virt)
	}
}

// TestStaleDecorationsMsgLeavesDiffStateAlone pins that the bufID guard still
// applies: a refresh for a buffer we've switched away from must not install
// its diff into the current one.
func TestStaleDecorationsMsgLeavesDiffStateAlone(t *testing.T) {
	m := virtTestModel(t, "kept\nreturn nil\n", nil)
	m.bufID = 7

	got, _ := m.Update(decorationsMsg{bufID: 99, items: []ClientDecoration{
		{Kind: ClientDecorationRemovedLine, Line: 1, Text: "from another buffer"},
	}})
	if virt := got.(Model).virtualLinesBefore(1); len(virt) != 0 {
		t.Errorf("stale decorationsMsg installed virtual rows %+v", virt)
	}
}

// TestGutterHasTwoModes covers the two looks the gutter switches between.
//
// With the inline diff off, a marked line shows an unobtrusive colour block
// and keeps its normal grey line number — this is the permanent
// while-editing state, so it must not tint the whole gutter. With the diff
// on, the same line shows git's "+" glyph and its number takes the marker's
// colour, so the row reads as one coloured unit.
func TestGutterHasTwoModes(t *testing.T) {
	const green = "#44BB44"
	const greenSGR = "38;2;68;187;68" // #44BB44 as a truecolor foreground

	base := func() Model {
		m := virtTestModel(t, "added line\nplain line\n", nil)
		m.reservePluginGutter = true
		m.cursor.Line = 1 // keep the cursor off the line under test
		return m
	}

	t.Run("diff off: colour block, plain number", func(t *testing.T) {
		m := base()
		m.decorations = []ClientDecoration{
			{Kind: ClientDecorationGutter, Line: 0, Text: "+", TextColor: green},
		}
		m = m.rebuildDiffDecorations()
		if m.inlineDiffOn {
			t.Fatal("premise: a gutter marker alone is not inline-diff mode")
		}

		cw := m.contentWidth()
		layout := m.buildScreenLayout(m.visibleLines(), cw)
		row := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

		if strings.Contains(ansi.Strip(row), "+") {
			t.Errorf("gutter = %q, want a colour block rather than the sign while the diff is off",
				ansi.Strip(row))
		}
		if !strings.Contains(row, "48;2;68;187;68") {
			t.Errorf("row = %q, want the marker drawn as a coloured block", row)
		}
		if strings.Contains(row, greenSGR+"m1 ") {
			t.Errorf("row = %q, want the line number left grey while the diff is off", row)
		}
	})

	t.Run("diff on: sign and coloured number", func(t *testing.T) {
		m := base()
		m.decorations = []ClientDecoration{
			{Kind: ClientDecorationGutter, Line: 0, Text: "+", TextColor: green},
			{Kind: ClientDecorationLineTint, Line: 0, Col: 0, EndCol: ^uint32(0), TextColor: "#16301C"},
		}
		m = m.rebuildDiffDecorations()
		if !m.inlineDiffOn {
			t.Fatal("premise: a tint means inline-diff mode is on")
		}

		cw := m.contentWidth()
		layout := m.buildScreenLayout(m.visibleLines(), cw)
		row := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

		if !strings.Contains(ansi.Strip(row), "+") {
			t.Errorf("gutter = %q, want the \"+\" sign while the diff is on", ansi.Strip(row))
		}
		if !strings.Contains(row, greenSGR) {
			t.Errorf("row = %q, want the marker colour applied to the line number too", row)
		}
	})
}

// TestDiffSignSitsBetweenNumberAndText pins the sign's position: it should
// have a space on each side rather than being jammed against the first
// character of the line.
func TestDiffSignSitsBetweenNumberAndText(t *testing.T) {
	m := virtTestModel(t, "added line\n", nil)
	m.reservePluginGutter = true
	m.cursor.Line = 0
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationGutter, Line: 0, Text: "+", TextColor: "#44BB44"},
		{Kind: ClientDecorationLineTint, Line: 0, Col: 0, EndCol: ^uint32(0), TextColor: "#16301C"},
	}
	m = m.rebuildDiffDecorations()

	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	plain := ansi.Strip(m.renderLineChunk(layout[0], cw, nil, -1, -1, false))

	if !strings.Contains(plain, " + ") {
		t.Errorf("gutter = %q, want the sign spaced on both sides (\" + \")", plain)
	}
}

// TestRemovedRowGutterShowsOldLineNumber covers the other half: a removed row
// is labelled with the line number it had in the pre-change file, which is
// what `git diff` labels removals with, plus a "-" sign.
func TestRemovedRowGutterShowsOldLineNumber(t *testing.T) {
	m := virtTestModel(t, "kept\n", nil)
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationRemovedLine, Line: 0, Text: "was here", OldLine: 42},
	}
	m.reservePluginGutter = true
	m = m.rebuildDiffDecorations()

	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	if !layout[0].isVirtual {
		t.Fatalf("row 0 = %+v, want the virtual removed row", layout[0])
	}

	plain := ansi.Strip(m.renderLineChunk(layout[0], cw, nil, -1, -1, false))
	if !strings.Contains(plain, "42") {
		t.Errorf("removed row = %q, want its pre-change line number 42", plain)
	}
	if !strings.Contains(plain, "-") {
		t.Errorf("removed row = %q, want the \"-\" sign", plain)
	}
	if !strings.Contains(plain, "was here") {
		t.Errorf("removed row = %q, want the removed content", plain)
	}
}

// TestRemovedRowWithoutOldLineLeavesNumberBlank pins the degradation path: a
// plugin that doesn't supply a pre-change line number gets a blank slot, not
// a misleading "0".
func TestRemovedRowWithoutOldLineLeavesNumberBlank(t *testing.T) {
	m := virtTestModel(t, "kept\n", nil)
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationRemovedLine, Line: 0, Text: "was here"}, // OldLine unset
	}
	m.reservePluginGutter = true
	m = m.rebuildDiffDecorations()

	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	plain := ansi.Strip(m.renderLineChunk(layout[0], cw, nil, -1, -1, false))
	if strings.Contains(plain, "0 ") {
		t.Errorf("removed row = %q, want a blank number slot rather than a literal 0", plain)
	}
	if !strings.Contains(plain, "was here") {
		t.Errorf("removed row = %q, want the removed content", plain)
	}
}

// TestCursorLineKeepsDiffAccent is a regression test for a reported bug: with
// the inline diff on, whichever line the cursor sat on showed a white line
// number instead of the diff colour, because the cursor-line style (a
// brighter grey) was checked before the accent and won.
//
// Both signals have to survive: the accent supplies the colour, bold marks
// the cursor's line.
func TestCursorLineKeepsDiffAccent(t *testing.T) {
	const greenSGR = "38;2;68;187;68" // #44BB44

	m := virtTestModel(t, "added line\nplain line\n", nil)
	m.reservePluginGutter = true
	m.cursor.Line = 0 // the line under test — the case that regressed
	m.decorations = []ClientDecoration{
		{Kind: ClientDecorationGutter, Line: 0, Text: "+", TextColor: "#44BB44"},
		{Kind: ClientDecorationLineTint, Line: 0, Col: 0, EndCol: ^uint32(0), TextColor: "#16301C"},
	}
	m = m.rebuildDiffDecorations()

	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	row := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

	if !strings.Contains(row, greenSGR) {
		t.Errorf("cursor line's number = %q, want the diff accent — the cursor-line "+
			"style is overriding it", row)
	}
	// Assert against what the style renders rather than a literal "1m":
	// lipgloss folds bold into one combined SGR ("\x1b[1;38;2;...m"), so
	// there is no standalone bold sequence to search for.
	want := lipgloss.NewStyle().Foreground(lipgloss.Color("#44BB44")).Bold(true).Render("1 ")
	if !strings.Contains(row, want) {
		t.Errorf("row = %q, want the cursor's line bolded (%q) so that signal survives too",
			row, want)
	}
}

// TestCursorLineWithoutDiffKeepsItsOwnStyle pins the other half: with no diff
// accent, the cursor line still gets the normal cursor-line gutter style.
func TestCursorLineWithoutDiffKeepsItsOwnStyle(t *testing.T) {
	m := virtTestModel(t, "plain\n", nil)
	m.cursor.Line = 0

	cw := m.contentWidth()
	layout := m.buildScreenLayout(m.visibleLines(), cw)
	row := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

	if !strings.Contains(row, gutterCurStyle.Render("1 ")) {
		t.Errorf("cursor line = %q, want the normal cursor-line gutter style", row)
	}
}
