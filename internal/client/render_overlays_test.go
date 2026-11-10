package client

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// TestBuildSearchOverlaysSkipsStaleMatchPastEndOfLine is a regression test
// for a crash: search matches are computed once, at search time, and are
// never recalculated when the buffer changes afterward — e.g. an LSP
// refactor edit (Extract Function) shrinking a line out from under a still-
// active search. Rendering used the stale match's column directly as a
// slice index into the line's current (now shorter) runes with no bounds
// check, panicking with "slice bounds out of range" the next time the view
// redrew. The match must be skipped instead.
func TestBuildSearchOverlaysSkipsStaleMatchPastEndOfLine(t *testing.T) {
	m := newTestModel("short\n")
	// Recorded before an edit shrank this line down to "short" (5 runes) —
	// column 9 no longer exists on the line.
	m.searchMatches = []substituteMatch{{line: 0, col: 9, length: 5}}
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw) // must not panic
	if len(rows) != 1 || len(rows[0]) != 0 {
		t.Errorf("buildSearchOverlays() = %v, want one row with no overlays (stale match skipped)", rows)
	}
}

// TestBuildSearchOverlaysRendersValidMatch is a sanity check alongside the
// regression test above: a match that still fits within its line must
// still render normally, so the staleness guard isn't over-broad.
func TestBuildSearchOverlaysRendersValidMatch(t *testing.T) {
	m := newTestModel("hello world\n")
	m.searchMatches = []substituteMatch{{line: 0, col: 6, length: 5}} // "world"
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("buildSearchOverlays() = %v, want one row with one overlay", rows)
	}
	if rows[0][0].col != 6 || rows[0][0].w != 5 {
		t.Errorf("overlay = %+v, want col=6 w=5", rows[0][0])
	}
}

// TestBuildSearchOverlaysSkipsMatchOnDeletedLine covers the buffer shrinking
// in *line count* (not just line length) out from under a stale match.
func TestBuildSearchOverlaysSkipsMatchOnDeletedLine(t *testing.T) {
	m := newTestModel("only line\n")
	m.searchMatches = []substituteMatch{{line: 5, col: 2, length: 3}} // line 5 no longer exists
	m.searchIdx = -1
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildSearchOverlays(layout, cw) // must not panic
	if len(rows) != 1 || len(rows[0]) != 0 {
		t.Errorf("buildSearchOverlays() = %v, want one row with no overlays", rows)
	}
}

// TestOverlayRulerColumnUsesRealGlyphAtBoundaryColumn is a regression test
// for an off-by-one that existed in the old lineOverlay-based ruler
// implementation: RulerColumn is 1-indexed, so the 0-based index into a
// line's runes is RulerColumn-1. A line whose length exactly equals
// RulerColumn does have a real character at that index (it's the line's
// last one) — overlayRulerColumn (a plain string splice) has no such
// boundary condition to get wrong, but this keeps the case covered.
func TestOverlayRulerColumnUsesRealGlyphAtBoundaryColumn(t *testing.T) {
	got := ansi.Strip(overlayRulerColumn("123456789X", 9)) // 0-based col 9 = 'X'
	if want := "123456789X"; got != want {
		t.Errorf("overlayRulerColumn at line-length boundary = %q, want %q (the real trailing character retained, just restyled)", got, want)
	}
}

// TestOverlayRulerColumnBlankPastEndOfLine is a sanity check alongside the
// regression test above: a line shorter than the ruler column gets padded
// with a blank placeholder rather than an out-of-range read/panic.
func TestOverlayRulerColumnBlankPastEndOfLine(t *testing.T) {
	got := ansi.Strip(overlayRulerColumn("short", 9))
	if want := "short     "; got != want { // "short" + 4 padding spaces to reach col 9, then the ruler's own blank glyph
		t.Errorf("overlayRulerColumn past end of line = %q, want %q", got, want)
	}
}

// TestApplyRulerColumnStaysFixedDespiteInlayHint is a regression test for
// the bug this rewrite fixes: the ruler used to be a lineOverlay positioned
// by buffer column, but an inlay hint earlier on the same row is a w:0
// overlay — it renders real, visible glyphs without advancing the
// buffer-column bookkeeping those overlays compete on, so the ruler ended
// up rendered further right on the terminal than intended, by exactly the
// hint's rendered width (confirmed by temporarily reverting to the old
// lineOverlay-based implementation, which fails this test). What lands
// under the ruler can legitimately differ line to line (it might now be
// part of the hint's own text) — what must never change is that exactly
// rulerCol real visual columns precede it, wherever that content came from.
func TestApplyRulerColumnStaysFixedDespiteInlayHint(t *testing.T) {
	render := func(withHint bool) string {
		m := newTestModel("let x = 1\n")
		m.cfg = &config.Config{InlayHints: true, RulerColumn: 7} // 0-based col 6
		if withHint {
			m.inlayHints = []ClientInlayHint{
				{Line: 0, Col: 5, Label: ": number", Kind: 1, PaddingLeft: true},
			}
		}
		cw := 80
		layout := m.buildScreenLayout(1, cw)
		rowOverlays := m.buildRowOverlays(layout, cw)
		if inlayOverlays := m.buildInlayHintOverlays(layout, cw); inlayOverlays != nil {
			for i := range layout {
				if len(inlayOverlays[i]) > 0 {
					rowOverlays[i] = mergeOverlays(rowOverlays[i], inlayOverlays[i])
				}
			}
		}
		lines := make([]string, len(layout))
		for i := range layout {
			lines[i] = m.renderLineChunk(layout[i], cw, rowOverlays[i], -1, -1, false)
		}
		m.applyRulerColumn(lines, layout, cw)
		return lines[0]
	}

	without := render(false)
	with := render(true)

	if ansi.Strip(without) == ansi.Strip(with) {
		t.Fatal("sanity check failed: the inlay hint didn't actually change the rendered row")
	}
	for name, line := range map[string]string{"without hint": without, "with hint": with} {
		if w := lipgloss.Width(ansi.Truncate(line, 6, "")); w != 6 {
			t.Errorf("%s: %d real visual columns precede the ruler cell, want exactly 6 (ruler column drifted)", name, w)
		}
	}
}

// TestApplyRulerColumnSkipsCursorCell verifies the ruler never overwrites
// a painted cursor's cell — otherwise the cursor would be invisible whenever
// it coincides with the ruler column. In command mode the buffer cursor is
// painted into the row (paintsBufferCursor).
func TestApplyRulerColumnSkipsCursorCell(t *testing.T) {
	m := newTestModel("0123456789\n")
	m.cfg = &config.Config{RulerColumn: 8} // 0-based col 7
	m.mode = ModeCommand
	m.cursor.Col = 7
	cw := 80
	layout := m.buildScreenLayout(1, cw)
	lines := []string{m.renderLineChunk(layout[0], cw, nil, m.cursor.Line, m.cursor.Col, true)}
	before := lines[0]
	m.applyRulerColumn(lines, layout, cw)
	if lines[0] != before {
		t.Errorf("ruler overwrote the cursor's cell: before = %q, after = %q", before, lines[0])
	}
}

// TestApplyRulerColumnSkipsExtraCursorCell is a regression test: the ruler's
// cursor-cell skip only ever checked the primary cursor, so a multi-cursor
// extra cursor (Ctrl+D, C, Alt+s) sitting on the ruler column would vanish
// under the ruler's fixed background even though the primary cursor
// elsewhere was correctly protected.
func TestApplyRulerColumnSkipsExtraCursorCell(t *testing.T) {
	m := newTestModel("0123456789\nabcdefghij\n")
	m.cfg = &config.Config{RulerColumn: 8} // 0-based col 7
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 1, Col: 7}, goalCol: -1}}
	cw := 80
	layout := m.buildScreenLayout(2, cw)
	lines := []string{
		m.renderLineChunk(layout[0], cw, nil, -1, -1, false),
		m.renderLineChunk(layout[1], cw, nil, -1, -1, false),
	}
	beforeRow0, beforeRow1 := lines[0], lines[1]
	m.applyRulerColumn(lines, layout, cw)
	if lines[1] != beforeRow1 {
		t.Errorf("ruler overwrote the extra cursor's cell: before = %q, after = %q", beforeRow1, lines[1])
	}
	// Sanity check: the ruler does still draw on the row without a cursor
	// on its column, so the skip above is actually exercising the ruler
	// rather than it having failed to draw anywhere.
	if lines[0] == beforeRow0 {
		t.Error("sanity check failed: ruler didn't render on the row with no cursor on its column")
	}
}

// rulerMarks renders m's frame with the ruler drawn as "|" (a style with
// no colour, so it shows in a test) and returns the frame's buffer rows.
// It swaps the package-level rulerStyle for the test's duration, so a test
// that calls it must not use t.Parallel.
func rulerMarks(t *testing.T, m Model) []string {
	t.Helper()
	old := rulerStyle
	rulerStyle = lipgloss.NewStyle().Transform(func(string) string { return "|" })
	t.Cleanup(func() { rulerStyle = old })
	out, _ := m.renderFrame()
	rows := strings.Split(out, "\n")
	rows = rows[:len(rows)-1] // the status bar
	for i := range rows {
		rows[i] = ansi.Strip(rows[i])
	}
	return rows
}

// TestRulerIsUnbrokenOnEveryRow is the reported gap: the ruler skipped a
// wrapped line's continuation rows and the rows past the end of the buffer.
func TestRulerIsUnbrokenOnEveryRow(t *testing.T) {
	m := newTestModel("short\n" + strings.Repeat("y", 70) + "\nok\n")
	m.cfg = &config.Config{RulerColumn: 20}
	m.width, m.height = 40, 10
	rows := rulerMarks(t, m)
	for i, row := range rows {
		if r := []rune(row); len(r) < 20 || r[19] != '|' {
			t.Errorf("row %d = %q, want the ruler in column 20", i, row)
		}
	}
}

// TestRulerIsNotHiddenUnderTheTerminalCursor: Normal mode's cursor is the
// terminal's own, drawn over the ruler, so skipping its cell only left a
// gap there.
func TestRulerIsNotHiddenUnderTheTerminalCursor(t *testing.T) {
	m := newTestModel("0123456789\n")
	m.cfg = &config.Config{RulerColumn: 8}
	m.cursor.Col = 7
	if row := rulerMarks(t, m)[0]; !strings.HasPrefix(row, "0123456|89") {
		t.Errorf("row = %q, want the ruler under the Normal-mode cursor", row)
	}
}

// TestRulerSkipsThePaintedBlockNotTheRawCursor: with a forward selection the
// cursor is drawn on the last selected character (cursorCell), one left of
// m.cursor; the ruler must step around that cell, not the one after it.
func TestRulerSkipsThePaintedBlockNotTheRawCursor(t *testing.T) {
	m := newTestModel("0123456789\n")
	m.cfg = &config.Config{RulerColumn: 8} // 0-based col 7
	m.mode = ModeCommand
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 2}, Head: document.Pos{Line: 0, Col: 8}}
	m.cursor = m.sel.Head
	cw := 80
	layout := m.buildScreenLayout(1, cw)
	lines := []string{m.renderLineChunk(layout[0], cw, nil, -1, -1, false)}
	before := lines[0]
	m.applyRulerColumn(lines, layout, cw)
	if lines[0] != before {
		t.Errorf("ruler drew over the painted cursor on column 8")
	}
}

// TestRulerTintsAWideCharacterWhole: a wide character covering the ruler's
// column used to be cut in half and dropped from the row.
func TestRulerTintsAWideCharacterWhole(t *testing.T) {
	for _, col := range []int{2, 3} { // 本 covers columns 2 and 3
		if got := ansi.Strip(overlayRulerColumn("日本語x", col)); got != "日本語x" {
			t.Errorf("ruler at column %d: row = %q, want %q", col, got, "日本語x")
		}
	}
}

// TestNoRulerInAWindowNarrowerThanIt: it used to show only on wrapped
// continuation rows, at an offset, as broken segments.
func TestNoRulerInAWindowNarrowerThanIt(t *testing.T) {
	m := newTestModel(strings.Repeat("y", 70) + "\n")
	m.cfg = &config.Config{RulerColumn: 50}
	m.width, m.height = 40, 6
	for i, row := range rulerMarks(t, m) {
		if strings.Contains(row, "|") {
			t.Errorf("row %d = %q, want no ruler", i, row)
		}
	}
}
