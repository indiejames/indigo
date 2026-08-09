package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

func newIndentGuideTestModel(content string) Model {
	m := newTestModel(content)
	m.cfg = &config.Config{IndentGuides: true}
	return m
}

// guideCols returns the columns carrying a guide overlay on row.
func guideCols(rows [][]lineOverlay, row int) []int {
	var cols []int
	for _, ovl := range rows[row] {
		cols = append(cols, ovl.col)
	}
	return cols
}

// screenRowForBufLine finds the screen row for the first chunk of bufLine.
func screenRowForBufLine(t *testing.T, layout []layoutEntry, bufLine int) int {
	t.Helper()
	for row, entry := range layout {
		if entry.bufLine == bufLine {
			return row
		}
	}
	t.Fatalf("could not find screen row for buffer line %d", bufLine)
	return -1
}

// TestBuildIndentGuideOverlaysBlankLineBorrowsSurroundingIndent verifies a
// blank line between two equally-indented lines still shows the guide that
// flows through the gap, matching what surrounds it.
func TestBuildIndentGuideOverlaysBlankLineBorrowsSurroundingIndent(t *testing.T) {
	m := newIndentGuideTestModel("func foo() {\n    x := 1\n\n    y := 2\n}\n")
	cw := 80
	layout := m.buildScreenLayout(m.buf.LineCount(), cw)

	rows := m.buildIndentGuideOverlays(layout, cw)

	blankRow := screenRowForBufLine(t, layout, 2)
	got := guideCols(rows, blankRow)
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("blank line guide cols = %v, want [0]", got)
	}
}

// TestBuildIndentGuideOverlaysBlankLineBeforeDedentKeepsEnclosingGuide
// verifies a run of blank lines right before a closing brace still shows the
// guide for the block that brace closes — the guide should flow all the way
// to the line above the brace, not vanish as soon as a shallower line is
// found looking forward.
func TestBuildIndentGuideOverlaysBlankLineBeforeDedentKeepsEnclosingGuide(t *testing.T) {
	m := newIndentGuideTestModel("func foo() {\n    x := 1\n\n}\n")
	cw := 80
	layout := m.buildScreenLayout(m.buf.LineCount(), cw)

	rows := m.buildIndentGuideOverlays(layout, cw)

	blankRow := screenRowForBufLine(t, layout, 2)
	got := guideCols(rows, blankRow)
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("blank line guide cols = %v, want [0]", got)
	}
}

// TestBuildIndentGuideOverlaysSkipsZeroIndentLine verifies a non-blank line
// with no leading whitespace still gets no guide (unchanged pre-existing
// behavior, kept as a baseline against the blank-line change above).
func TestBuildIndentGuideOverlaysSkipsZeroIndentLine(t *testing.T) {
	m := newIndentGuideTestModel("func foo() {\n}\n")
	cw := 80
	layout := m.buildScreenLayout(m.buf.LineCount(), cw)

	rows := m.buildIndentGuideOverlays(layout, cw)
	for row, entry := range layout {
		if entry.bufLine == 0 || entry.bufLine == 1 {
			if got := guideCols(rows, row); len(got) != 0 {
				t.Errorf("row %d (bufLine %d) guide cols = %v, want none", row, entry.bufLine, got)
			}
		}
	}
}

// TestBuildIndentGuideOverlaysUsesBufferIndentWidth reproduces the reported
// bug against a 2-space-indented TypeScript file: guide spacing must follow
// the file's own indent width (2), not a hardcoded 4, and a nested block's
// guide must appear even across a blank line inside it.
func TestBuildIndentGuideOverlaysUsesBufferIndentWidth(t *testing.T) {
	content := "function coco() {\n" + // 0
		"  console.log('Hello');\n" + // 1
		"  for (let i = 0; i < 4; i++) {\n" + // 2
		"\n" + // 3 (blank, inside for loop)
		"    console.log(i);\n" + // 4
		"  }\n" + // 5
		"\n" + // 6 (blank, before closing brace)
		"}\n" // 7
	m := newIndentGuideTestModel(content)
	m.filePath = "test.ts" // 2-space indent default for "ts"
	cw := 80
	layout := m.buildScreenLayout(m.buf.LineCount(), cw)

	rows := m.buildIndentGuideOverlays(layout, cw)

	// Line 3 is blank but sits inside the for-loop body (next non-blank line
	// is indented 4), so it must show both the function-level (col 0) and
	// for-loop-level (col 2) guides.
	row3 := screenRowForBufLine(t, layout, 3)
	if got := guideCols(rows, row3); len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Errorf("blank line inside for-loop guide cols = %v, want [0 2]", got)
	}

	// Line 6 is blank, directly before the function's closing brace; it must
	// still show the function-level guide (col 0), carried forward from the
	// indented content above it.
	row6 := screenRowForBufLine(t, layout, 6)
	if got := guideCols(rows, row6); len(got) != 1 || got[0] != 0 {
		t.Errorf("blank line before closing brace guide cols = %v, want [0]", got)
	}
}
