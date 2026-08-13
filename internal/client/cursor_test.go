package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// --- moveCursor ---

func TestMoveCursorDown(t *testing.T) {
	m := newTestModel("line1\nline2\nline3\n")
	m.moveCursor(1, 0)
	if m.cursor.Line != 1 {
		t.Errorf("cursor.Line = %d, want 1", m.cursor.Line)
	}
}

func TestMoveCursorClampsBelowBuffer(t *testing.T) {
	m := newTestModel("line1\nline2\n")
	m.moveCursor(99, 0)
	last := m.buf.LineCount() - 1
	if m.cursor.Line != last {
		t.Errorf("cursor.Line = %d, want %d", m.cursor.Line, last)
	}
}

func TestMoveCursorClampsAboveBuffer(t *testing.T) {
	m := newTestModel("abc\ndef\n")
	m.cursor.Line = 1
	m.moveCursor(-99, 0)
	if m.cursor.Line != 0 {
		t.Errorf("cursor.Line = %d, want 0", m.cursor.Line)
	}
}

func TestMoveCursorNormalModeClampsCol(t *testing.T) {
	// Normal mode: maxCol = lineLen when a following line exists, since the
	// cursor may rest on the line break itself.
	m := newTestModel("hello\n")
	m.moveCursor(0, 10)
	if m.cursor.Col != 5 {
		t.Errorf("cursor.Col = %d, want 5", m.cursor.Col)
	}
}

func TestMoveCursorNormalModeClampsColOnLastLine(t *testing.T) {
	// The last line of the buffer has no line break to rest on, so it still
	// clamps to the last character.
	m := newTestModel("hello")
	m.moveCursor(0, 10)
	if m.cursor.Col != 4 {
		t.Errorf("cursor.Col = %d, want 4", m.cursor.Col)
	}
}

func TestMoveCursorInsertModeAllowsEndCol(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.moveCursor(0, 10)
	// Insert mode: maxCol = lineLen = 5
	if m.cursor.Col != 5 {
		t.Errorf("cursor.Col = %d, want 5", m.cursor.Col)
	}
}

// --- clampCursor ---

func TestClampCursorBeyondBuffer(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 99, Col: 99}
	m.clampCursor()
	if m.cursor.Line >= m.buf.LineCount() {
		t.Errorf("cursor.Line = %d, should be < %d", m.cursor.Line, m.buf.LineCount())
	}
}

func TestClampCursorBeyondLineLen(t *testing.T) {
	m := newTestModel("hi\n")
	m.cursor = document.Pos{Line: 0, Col: 99}
	m.clampCursor()
	if m.cursor.Col > m.buf.LineLen(0) {
		t.Errorf("cursor.Col = %d, should be <= %d", m.cursor.Col, m.buf.LineLen(0))
	}
}

// --- scrollToCursor ---

func TestScrollToCursorScrollsDown(t *testing.T) {
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 4 // visibleLines = 3
	m.cursor.Line = 5
	m.topLine = 0
	m.scrollToCursor()
	if m.topLine == 0 {
		t.Error("topLine should have advanced to scroll cursor into view")
	}
	if m.cursor.Line < m.topLine || m.cursor.Line >= m.topLine+m.visibleLines() {
		t.Errorf("cursor %d not in view [%d, %d)", m.cursor.Line, m.topLine, m.topLine+m.visibleLines())
	}
}

func TestScrollToCursorScrollsUp(t *testing.T) {
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 4
	m.topLine = 4
	m.cursor.Line = 1
	m.scrollToCursor()
	if m.topLine != 1 {
		t.Errorf("topLine = %d, want 1", m.topLine)
	}
}

// --- displayLineCount ---

func TestDisplayLineCountWithTrailingNewline(t *testing.T) {
	m := newTestModel("a\nb\n")
	// LineCount=3 (3 lines: "a", "b", ""), trailing empty → displayLineCount=2
	got := m.displayLineCount()
	if got != 2 {
		t.Errorf("displayLineCount = %d, want 2", got)
	}
}

func TestDisplayLineCountNoTrailingNewline(t *testing.T) {
	m := newTestModel("a\nb")
	// LineCount=2 ("a", "b"), no trailing empty → displayLineCount=2
	got := m.displayLineCount()
	if got != 2 {
		t.Errorf("displayLineCount = %d, want 2", got)
	}
}

// --- selectionCols ---

func TestSelectionColsNoSelection(t *testing.T) {
	m := newTestModel("hello\n")
	a, b := m.selectionCols(0, 5)
	if a != -1 || b != -1 {
		t.Errorf("no sel: got (%d, %d), want (-1, -1)", a, b)
	}
}

func TestSelectionColsSameLine(t *testing.T) {
	m := newTestModel("hello world\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 0, Col: 6},
	}
	a, b := m.selectionCols(0, 11)
	if a != 2 || b != 6 {
		t.Errorf("selectionCols = (%d, %d), want (2, 6)", a, b)
	}
}

func TestSelectionColsLineNotSelected(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 4},
	}
	a, b := m.selectionCols(1, 5)
	if a != -1 || b != -1 {
		t.Errorf("unselected line: got (%d, %d), want (-1, -1)", a, b)
	}
}

func TestSelectionColsMultiLineStartLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 1, Col: 3},
	}
	a, b := m.selectionCols(0, 5) // start line: from col 2 to end
	if a != 2 {
		t.Errorf("start line selA = %d, want 2", a)
	}
	if b != 4 { // lineLen-1 = 4
		t.Errorf("start line selB = %d, want 4", b)
	}
}

func TestSelectionColsMultiLineEndLine(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 1, Col: 3},
	}
	a, b := m.selectionCols(1, 5) // end line: from col 0 to col 3
	if a != 0 || b != 3 {
		t.Errorf("end line selectionCols = (%d, %d), want (0, 3)", a, b)
	}
}

func TestSelectionColsLineSelection(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 4},
		IsLine: true,
	}
	a, b := m.selectionCols(0, 5)
	if a != 0 {
		t.Errorf("line sel selA = %d, want 0", a)
	}
	if b != 4 {
		t.Errorf("line sel selB = %d, want 4", b)
	}
}

func TestSelectionColsEmptyLine(t *testing.T) {
	m := newTestModel("hello\n\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
	}
	// Empty line inside the selection: still reports a (0,0) padding cell so
	// the line renders as selected instead of looking untouched.
	a, b := m.selectionCols(1, 0)
	if a != 0 || b != 0 {
		t.Errorf("empty line: got (%d, %d), want (0, 0)", a, b)
	}
}

func TestSelectionColsEmptyLineOutsideSelection(t *testing.T) {
	m := newTestModel("hello\n\nworld\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 4},
	}
	// Empty line not covered by the selection: still reports no selection.
	a, b := m.selectionCols(1, 0)
	if a != -1 || b != -1 {
		t.Errorf("empty line outside selection: got (%d, %d), want (-1, -1)", a, b)
	}
}

// --- soft-wrap helpers ---

func TestVisualChunks(t *testing.T) {
	cases := []struct {
		expandedLen, cw, want int
	}{
		{0, 80, 1},   // empty line still takes one row
		{80, 80, 1},  // exact fit
		{81, 80, 2},  // one char overflow → second chunk
		{160, 80, 2}, // exactly two chunks
		{161, 80, 3}, // three chunks
		{5, 80, 1},   // short line
	}
	for _, c := range cases {
		got := visualChunks(c.expandedLen, c.cw)
		if got != c.want {
			t.Errorf("visualChunks(%d, %d) = %d, want %d", c.expandedLen, c.cw, got, c.want)
		}
	}
}

func TestBuildScreenLayoutNoWrap(t *testing.T) {
	// Five short lines, each fits in one screen row.
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.height = 6 // vis = 5
	m.width = 80
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	if len(layout) != vis {
		t.Fatalf("layout len = %d, want %d", len(layout), vis)
	}
	for i, e := range layout {
		if e.bufLine != i || e.chunk != 0 || e.chunkStart != 0 {
			t.Errorf("layout[%d] = %+v, want {bufLine:%d chunk:0 chunkStart:0}", i, e, i)
		}
	}
}

func TestBuildScreenLayoutWraps(t *testing.T) {
	// One line that is 100 chars wide, viewport is 40 cols → 3 chunks.
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 6 // vis = 5
	m.width = 40 // cw = 40 (no gutter when LineNumbers disabled)
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)
	// 100 chars at cw=40 → ceil(100/40)=3 chunks.
	if layout[0].bufLine != 0 || layout[0].chunk != 0 || layout[0].chunkStart != 0 {
		t.Errorf("chunk0: %+v", layout[0])
	}
	if layout[1].bufLine != 0 || layout[1].chunk != 1 || layout[1].chunkStart != 40 {
		t.Errorf("chunk1: %+v", layout[1])
	}
	if layout[2].bufLine != 0 || layout[2].chunk != 2 || layout[2].chunkStart != 80 {
		t.Errorf("chunk2: %+v", layout[2])
	}
}

func TestScrollToCursorWrappedLine(t *testing.T) {
	// Line is 100 chars; viewport is 40 cols wide and 4 rows tall (vis=3).
	// Cursor at col 85 (chunk 2, chunkStart 80) — the third visual row.
	// With topLine=0 the cursor is at visual row 2, which fits in vis=3. No scroll needed.
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 4
	m.width = 40
	m.cursor.Line = 0
	m.cursor.Col = 85
	m.topLine = 0
	m.scrollToCursor()
	if m.topLine != 0 {
		t.Errorf("topLine = %d, want 0 (cursor fits in first 3 visual rows)", m.topLine)
	}
}

func TestCursorVisualRowFromTop(t *testing.T) {
	// Short lines: cursor at buffer line 2 is visual row 2 from topLine 0.
	m := newTestModel("a\nb\nc\nd\n")
	m.width = 80
	m.cursor.Line = 2
	m.topLine = 0
	got := m.cursorVisualRowFromTop(m.contentWidth())
	if got != 2 {
		t.Errorf("short lines: cursorVisualRowFromTop = %d, want 2", got)
	}
}

// TestGoToLastLineShowsWrappedTail is a regression test: jumping to the
// last line (G) lands the cursor at column 0 (its first, topmost visual
// chunk), which scrollToCursor alone considers already "visible" without
// scrolling further — stranding the rest of a wrapped final line below the
// viewport with no line after it to move the cursor into and trigger
// further scrolling.
func TestGoToLastLineShowsWrappedTail(t *testing.T) {
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	// "a\nb\nc\n" + a 100-char final line with no trailing newline, so the
	// long line is the true last buffer line (not a phantom empty one).
	m := newTestModel("a\nb\nc\n" + string(longLine))
	m.height = 5 // vis = 4
	m.width = 40 // cw = 40 → the last line wraps into 3 chunks

	mi, _ := executeGoToLastLine(m)
	m2 := mi.(Model)

	cw := m2.contentWidth()
	vis := m2.visibleLines()
	layout := m2.buildScreenLayout(vis, cw)

	lastLine := m2.buf.LineCount() - 1
	sawChunk := map[int]bool{}
	for _, e := range layout {
		if e.bufLine == lastLine {
			sawChunk[e.chunk] = true
		}
	}
	for _, c := range []int{0, 1, 2} {
		if !sawChunk[c] {
			t.Errorf("chunk %d of the wrapped last line is not visible; layout = %+v", c, layout)
		}
	}
	if screenRowOf(layout, m2.cursor.Line, 0, cw) < 0 {
		t.Error("cursor's own row is not visible after scrolling to show the wrapped tail")
	}
}

// TestGoToEndCursorStaysVisibleWhenPrecedingLineOverflowsBudget is a
// regression test for a pre-existing bug in findTopLineForRow: when the
// line just above the target overflows the remaining row budget on its
// own, using it as topLine renders ALL of its wrap chunks (topLine can only
// start a line at its first chunk — there's no way to scroll to "line X,
// chunk 3"), consuming more rows than intended and pushing the actual
// target (here, the cursor on the final, empty phantom line after a long
// wrapped line) out of the rendered layout entirely — the cursor just
// disappears.
func TestGoToEndCursorStaysVisibleWhenPrecedingLineOverflowsBudget(t *testing.T) {
	longLine := make([]byte, 200)
	for i := range longLine {
		longLine[i] = 'x'
	}
	// Trailing \n makes the true last buffer line an empty phantom line,
	// immediately preceded by a line that wraps into 5 chunks at cw=40.
	m := newTestModel("a\nb\nc\n" + string(longLine) + "\n")
	m.height = 5 // vis = 4; rowsAbove for the phantom line's chunk 0 is 3 < 5
	m.width = 40

	mi, _ := executeGoToEnd(m)
	m2 := mi.(Model)

	cw := m2.contentWidth()
	vis := m2.visibleLines()
	layout := m2.buildScreenLayout(vis, cw)
	if row := screenRowOf(layout, m2.cursor.Line, 0, cw); row < 0 {
		t.Fatalf("cursor not visible after go-to-end: topLine=%d cursor=%+v layout=%+v", m2.topLine, m2.cursor, layout)
	}
}

// TestGoToLastLineWithMoreChunksThanViewport is a regression test for a final
// line that wraps into more chunks than the viewport can ever show at once
// (6 chunks at vis=3). executeGoToLastLine puts the cursor at column 0 of
// that line (its first chunk), matching G's usual "start of line" semantics
// (see executeGoToLineStart) — so cursor visibility and tail visibility are
// mutually exclusive here: showing the tail (chunks 3-5) would scroll chunk
// 0, where the cursor sits, off-screen. Cursor visibility wins: the view
// shows chunks 0-2, and scrollToShowLineTail's clamp (in render.go) is a
// no-op rather than stranding the cursor.
func TestGoToLastLineWithMoreChunksThanViewport(t *testing.T) {
	// Create a final line that wraps into 6 chunks at width 20
	longLine := make([]byte, 120)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel("a\nb\n" + string(longLine))
	m.height = 4 // vis = 3, but the final line needs 6 chunks
	m.width = 20

	mi, _ := executeGoToLastLine(m)
	m2 := mi.(Model)

	cw := m2.contentWidth()
	vis := m2.visibleLines()
	layout := m2.buildScreenLayout(vis, cw)

	lastLine := m2.buf.LineCount() - 1
	sawChunk := map[int]bool{}
	for _, e := range layout {
		if e.bufLine == lastLine {
			sawChunk[e.chunk] = true
		}
	}

	// The cursor's chunk (0, since Col is 0) must stay visible.
	if row := screenRowOf(layout, m2.cursor.Line, 0, cw); row < 0 {
		t.Fatalf("cursor not visible; topLine=%d topChunk=%d cursor=%+v layout=%+v", m2.topLine, m2.topChunk, m2.cursor, layout)
	}

	// With chunk 0 anchored, chunks 0-2 fill the 3-row viewport; the tail
	// (chunks 3-5) is unreachable without hiding the cursor.
	for _, chunk := range []int{0, 1, 2} {
		if !sawChunk[chunk] {
			t.Errorf("expected chunk %d of the last line to be visible; saw chunks %v, layout=%+v", chunk, sawChunk, layout)
		}
	}
	for _, chunk := range []int{3, 4, 5} {
		if sawChunk[chunk] {
			t.Errorf("chunk %d should not be visible — showing it would scroll the cursor's chunk 0 off-screen; saw chunks %v", chunk, sawChunk)
		}
	}

	if m2.topChunk != 0 {
		t.Errorf("topChunk = %d, want 0 (anchored on the cursor's own chunk)", m2.topChunk)
	}
}

func TestScreenRowOf(t *testing.T) {
	longLine := make([]byte, 100)
	for i := range longLine {
		longLine[i] = 'x'
	}
	m := newTestModel(string(longLine) + "\n")
	m.height = 6
	m.width = 40
	cw := m.contentWidth()
	vis := m.visibleLines()
	layout := m.buildScreenLayout(vis, cw)

	// visCol=0 → chunk 0 → row 0
	if r := screenRowOf(layout, 0, 0, cw); r != 0 {
		t.Errorf("visCol=0: row=%d, want 0", r)
	}
	// visCol=40 → chunk 1 → row 1
	if r := screenRowOf(layout, 0, 40, cw); r != 1 {
		t.Errorf("visCol=40: row=%d, want 1", r)
	}
	// visCol=80 → chunk 2 → row 2
	if r := screenRowOf(layout, 0, 80, cw); r != 2 {
		t.Errorf("visCol=80: row=%d, want 2", r)
	}
	// bufLine not in layout → -1
	if r := screenRowOf(layout, 99, 0, cw); r != -1 {
		t.Errorf("missing bufLine: row=%d, want -1", r)
	}
}

// --- moveCursorChar ---

func TestMoveCursorCharRightRestsOnLineBreak(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 2} // on the second 'o'
	m.moveCursorChar(1)
	if m.cursor != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:3} (on the line break)", m.cursor)
	}
}

func TestMoveCursorCharRightCrossesLineBreak(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 3} // resting on the line break
	m.moveCursorChar(1)
	if m.cursor != (document.Pos{Line: 1, Col: 0}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:0}", m.cursor)
	}
}

func TestMoveCursorCharLeftEntersLineBreak(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.moveCursorChar(-1)
	if m.cursor != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:3} (back on the line break)", m.cursor)
	}
}

func TestMoveCursorCharRightStopsAtTrueEOF(t *testing.T) {
	m := newTestModel("foo") // no trailing newline: nothing past the last char
	m.cursor = document.Pos{Line: 0, Col: 2}
	m.moveCursorChar(1)
	if m.cursor != (document.Pos{Line: 0, Col: 2}) {
		t.Errorf("cursor = %+v, want unchanged {Line:0 Col:2}", m.cursor)
	}
}

func TestMoveCursorCharLeftStopsAtBufferStart(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.moveCursorChar(-1)
	if m.cursor != (document.Pos{Line: 0, Col: 0}) {
		t.Errorf("cursor = %+v, want unchanged {Line:0 Col:0}", m.cursor)
	}
}

// TestDeleteOnLineBreakJoinsLines is a regression test for navigating the
// cursor onto a line break with moveCursorChar and deleting it with 'd',
// treating the line break like any other character.
func TestDeleteOnLineBreakJoinsLines(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 2} // on the second 'o'
	m.moveCursorChar(1)                      // now resting on the line break
	got, _ := m.handleNormal(fakeKey("d"))
	m2 := got.(Model)
	if line := m2.buf.Line(0); line != "foobar" {
		t.Errorf("Line(0) = %q, want %q", line, "foobar")
	}
	if m2.cursor != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:3}", m2.cursor)
	}
}
