package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestExecuteGoToLineStartMovesAllCursors is a regression test: gh must move
// every cursor to its own line start, not just the primary cursor.
func TestExecuteGoToLineStartMovesAllCursors(t *testing.T) {
	m := newTestModel("  abc\n  def\n")
	m.cursor = document.Pos{Line: 0, Col: 4}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 2}, Head: document.Pos{Line: 0, Col: 4}}
	m.extraCursors = []ExtraCursor{{
		pos: document.Pos{Line: 1, Col: 4},
		sel: &Selection{Anchor: document.Pos{Line: 1, Col: 2}, Head: document.Pos{Line: 1, Col: 4}},
	}}

	m2, _ := executeGoToLineStart(m)
	got := m2.(Model)

	if got.cursor.Col != 0 {
		t.Errorf("primary cursor.Col = %d, want 0", got.cursor.Col)
	}
	if got.sel != nil {
		t.Errorf("primary sel = %v, want nil", got.sel)
	}
	if got.extraCursors[0].pos.Col != 0 {
		t.Errorf("extra cursor.Col = %d, want 0", got.extraCursors[0].pos.Col)
	}
	if got.extraCursors[0].sel != nil {
		t.Errorf("extra cursor.sel = %v, want nil", got.extraCursors[0].sel)
	}
}

// TestExecuteGoToLineEndMovesAllCursors is a regression test: gl must move
// every cursor to its own line end, not just the primary cursor.
func TestExecuteGoToLineEndMovesAllCursors(t *testing.T) {
	m := newTestModel("ab\nabcde\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 1}}
	m.extraCursors = []ExtraCursor{{
		pos: document.Pos{Line: 1, Col: 0},
		sel: &Selection{Anchor: document.Pos{Line: 1, Col: 0}, Head: document.Pos{Line: 1, Col: 1}},
	}}

	m2, _ := executeGoToLineEnd(m)
	got := m2.(Model)

	if got.cursor.Col != 2 {
		t.Errorf("primary cursor.Col = %d, want 2", got.cursor.Col)
	}
	if got.sel != nil {
		t.Errorf("primary sel = %v, want nil", got.sel)
	}
	if got.extraCursors[0].pos.Col != 5 {
		t.Errorf("extra cursor.Col = %d, want 5", got.extraCursors[0].pos.Col)
	}
	if got.extraCursors[0].sel != nil {
		t.Errorf("extra cursor.sel = %v, want nil", got.extraCursors[0].sel)
	}
}

// TestExecuteSelectInsideCharExcludesTrailingDelimiter is a regression test:
// mi. must select only the quoted content, not the closing delimiter.
func TestExecuteSelectInsideCharExcludesTrailingDelimiter(t *testing.T) {
	m := newTestModel(`var s = "hello"` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 11} // inside "hello"

	got, _ := executeSelectInsideChar(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideChar: sel is nil")
	}
	if text := m2.selectedText(); text != "hello" {
		t.Errorf("selectedText() = %q, want %q", text, "hello")
	}
}

// TestExecuteSelectInsideCharEmptyQuotes verifies the empty-content edge
// case ("") doesn't invert into a selection spanning both delimiters.
func TestExecuteSelectInsideCharEmptyQuotes(t *testing.T) {
	m := newTestModel(`var s = ""` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 8} // on the opening quote

	got, _ := executeSelectInsideChar(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideChar: sel is nil")
	}
	start, end := m2.sel.ordered()
	if start.Col > end.Col {
		t.Errorf("selection inverted: start=%v end=%v", start, end)
	}
	if text := m2.selectedText(); text == `""` {
		t.Errorf("selectedText() = %q, want it not to include both delimiters", text)
	}
}

// TestExecuteSelectInsideWhitespaceSelectsRun verifies mis selects a
// contiguous run of leading indentation, not just a single space.
func TestExecuteSelectInsideWhitespaceSelectsRun(t *testing.T) {
	m := newTestModel("  foo\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeSelectInsideWhitespace(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideWhitespace: sel is nil")
	}
	if text := m2.selectedText(); text != "  " {
		t.Errorf("selectedText() = %q, want %q", text, "  ")
	}
}

// TestExecuteSelectInsideWhitespaceNoopOnNonSpace verifies mis leaves the
// selection untouched when the cursor isn't on whitespace.
func TestExecuteSelectInsideWhitespaceNoopOnNonSpace(t *testing.T) {
	m := newTestModel("foo\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeSelectInsideWhitespace(m)
	m2 := got.(Model)

	if m2.sel != nil {
		t.Errorf("executeSelectInsideWhitespace: sel = %v, want nil", m2.sel)
	}
}

// TestExecuteSelectInsideBracketsExcludesTrailingBracket is a regression
// test: mim must select only the bracketed content, not the closing bracket.
func TestExecuteSelectInsideBracketsExcludesTrailingBracket(t *testing.T) {
	m := newTestModel(`var n = (foo)` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 12} // on the closing paren

	got, _ := executeSelectInsideBrackets(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideBrackets: sel is nil")
	}
	if text := m2.selectedText(); text != "foo" {
		t.Errorf("selectedText() = %q, want %q", text, "foo")
	}
}

// TestExecuteSelectInsideBracketsMultiLine verifies the close-bracket-at-
// column-0 case correctly extends through the end of the previous line
// instead of an invalid negative column.
func TestExecuteSelectInsideBracketsMultiLine(t *testing.T) {
	m := newTestModel("(\n  foo\n)\n")
	m.cursor = document.Pos{Line: 1, Col: 3} // inside "  foo"

	got, _ := executeSelectInsideBrackets(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideBrackets: sel is nil")
	}
	if text := m2.selectedText(); text != "\n  foo" {
		t.Errorf("selectedText() = %q, want %q", text, "\n  foo")
	}
}

// TestExecuteSelectInsideBracketsEmpty verifies adjacent brackets ("()")
// don't invert into a selection spanning both brackets.
func TestExecuteSelectInsideBracketsEmpty(t *testing.T) {
	m := newTestModel(`var n = ()` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 9} // on the closing paren

	got, _ := executeSelectInsideBrackets(m)
	m2 := got.(Model)

	if m2.sel == nil {
		t.Fatal("executeSelectInsideBrackets: sel is nil")
	}
	start, end := m2.sel.ordered()
	if start.Col > end.Col {
		t.Errorf("selection inverted: start=%v end=%v", start, end)
	}
	if text := m2.selectedText(); text == "()" {
		t.Errorf("selectedText() = %q, want it not to include both brackets", text)
	}
}

// --- matchingPairPos ---

func TestMatchingPairPosOpenBracketFindsClose(t *testing.T) {
	m := newTestModel("foo(bar)\n")
	m.cursor = document.Pos{Line: 0, Col: 3} // on '('

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 0 || col != 7 {
		t.Errorf("matchingPairPos = (%d,%d), want (0,7) — the ')'", line, col)
	}
}

func TestMatchingPairPosCloseBracketFindsOpen(t *testing.T) {
	m := newTestModel("foo(bar)\n")
	m.cursor = document.Pos{Line: 0, Col: 7} // on ')'

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 0 || col != 3 {
		t.Errorf("matchingPairPos = (%d,%d), want (0,3) — the '('", line, col)
	}
}

func TestMatchingPairPosMultiLineBracket(t *testing.T) {
	m := newTestModel("func foo() {\n  return\n}\n")
	m.cursor = document.Pos{Line: 0, Col: 11} // on '{'

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 2 || col != 0 {
		t.Errorf("matchingPairPos = (%d,%d), want (2,0) — the '}'", line, col)
	}
}

func TestMatchingPairPosQuoteOpeningFindsClosing(t *testing.T) {
	m := newTestModel(`var s = "hello"` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 8} // opening quote

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 0 || col != 14 {
		t.Errorf("matchingPairPos = (%d,%d), want (0,14) — the closing quote", line, col)
	}
}

func TestMatchingPairPosQuoteClosingFindsOpening(t *testing.T) {
	m := newTestModel(`var s = "hello"` + "\n")
	m.cursor = document.Pos{Line: 0, Col: 14} // closing quote

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 0 || col != 8 {
		t.Errorf("matchingPairPos = (%d,%d), want (0,8) — the opening quote", line, col)
	}
}

// TestMatchingPairPosSecondQuotePair verifies quotes are paired positionally
// (1st-with-2nd, 3rd-with-4th) rather than "nearest same-char", so a second
// quoted string on the same line pairs with itself, not the first string's
// quotes.
func TestMatchingPairPosSecondQuotePair(t *testing.T) {
	m := newTestModel(`f("a", "b")` + "\n")
	// f(0) ((1) "(2) a(3) "(4) ,(5) (6) "(7) b(8) "(9) )(10)
	m.cursor = document.Pos{Line: 0, Col: 7} // opening quote of "b"

	line, col, ok := matchingPairPos(m)
	if !ok {
		t.Fatal("matchingPairPos: not found")
	}
	if line != 0 || col != 9 {
		t.Errorf("matchingPairPos = (%d,%d), want (0,9) — closing quote of \"b\", not \"a\"'s quotes", line, col)
	}
}

func TestMatchingPairPosNotOnPairCharacter(t *testing.T) {
	m := newTestModel("foo(bar)\n")
	m.cursor = document.Pos{Line: 0, Col: 1} // on 'o'

	if _, _, ok := matchingPairPos(m); ok {
		t.Error("matchingPairPos: found a match for a plain letter, want not found")
	}
}

func TestMatchingPairPosUnmatchedBracket(t *testing.T) {
	m := newTestModel("foo(bar\n")
	m.cursor = document.Pos{Line: 0, Col: 3} // on '(' with no closing ')'

	if _, _, ok := matchingPairPos(m); ok {
		t.Error("matchingPairPos: found a match for an unclosed bracket, want not found")
	}
}

func TestExecuteJoinLinesInsertsSpaceAndStripsIndent(t *testing.T) {
	m := newTestModel("foo\n    bar\n")
	m.cursor = document.Pos{Line: 0, Col: 1}

	got, _ := executeJoinLines(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo bar" {
		t.Errorf("Line(0) = %q, want %q", line, "foo bar")
	}
	if m2.buf.LineCount() != 2 {
		t.Errorf("LineCount() = %d, want 2", m2.buf.LineCount())
	}
	if m2.cursor != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:3}", m2.cursor)
	}
}

func TestExecuteJoinLinesNoExtraSpaceWhenLineEndsInWhitespace(t *testing.T) {
	m := newTestModel("foo \n  bar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeJoinLines(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo bar" {
		t.Errorf("Line(0) = %q, want %q", line, "foo bar")
	}
}

func TestExecuteJoinLinesNoSpaceBeforeCloseParen(t *testing.T) {
	m := newTestModel("foo(bar\n)\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeJoinLines(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo(bar)" {
		t.Errorf("Line(0) = %q, want %q", line, "foo(bar)")
	}
}

func TestExecuteJoinLinesEmptyCurrentLine(t *testing.T) {
	m := newTestModel("\n  bar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeJoinLines(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "bar" {
		t.Errorf("Line(0) = %q, want %q", line, "bar")
	}
}

func TestExecuteJoinLinesOnLastLineIsNoop(t *testing.T) {
	m := newTestModel("foo")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, cmd := executeJoinLines(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo" {
		t.Errorf("Line(0) = %q, want %q", line, "foo")
	}
	if cmd != nil {
		t.Error("cmd = non-nil, want nil for a no-op join")
	}
}

func TestExecuteMoveLineDownSwapsWithNextLine(t *testing.T) {
	m := newTestModel("foo\nbar\nbaz\n")
	m.cursor = document.Pos{Line: 0, Col: 2}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "bar" {
		t.Errorf("Line(0) = %q, want %q", line, "bar")
	}
	if line := m2.buf.Line(1); line != "foo" {
		t.Errorf("Line(1) = %q, want %q", line, "foo")
	}
	if line := m2.buf.Line(2); line != "baz" {
		t.Errorf("Line(2) = %q, want %q", line, "baz")
	}
	if m2.cursor != (document.Pos{Line: 1, Col: 2}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:2}", m2.cursor)
	}
}

func TestExecuteMoveLineUpSwapsWithPrevLine(t *testing.T) {
	m := newTestModel("foo\nbar\nbaz\n")
	m.cursor = document.Pos{Line: 1, Col: 1}

	got, _ := executeMoveLineUp(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "bar" {
		t.Errorf("Line(0) = %q, want %q", line, "bar")
	}
	if line := m2.buf.Line(1); line != "foo" {
		t.Errorf("Line(1) = %q, want %q", line, "foo")
	}
	if m2.cursor != (document.Pos{Line: 0, Col: 1}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:1}", m2.cursor)
	}
}

func TestExecuteMoveLineUpAtTopIsNoop(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, cmd := executeMoveLineUp(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo" {
		t.Errorf("Line(0) = %q, want %q", line, "foo")
	}
	if cmd != nil {
		t.Error("cmd = non-nil, want nil for a no-op move")
	}
}

func TestExecuteMoveLineDownAtBottomIsNoop(t *testing.T) {
	m := newTestModel("foo\nbar")
	m.cursor = document.Pos{Line: 1, Col: 0}

	got, cmd := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(1); line != "bar" {
		t.Errorf("Line(1) = %q, want %q", line, "bar")
	}
	if cmd != nil {
		t.Error("cmd = non-nil, want nil for a no-op move")
	}
}

func TestExecuteMoveLineDownNoTrailingNewlineAtEOF(t *testing.T) {
	m := newTestModel("foo\nbar")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "bar" {
		t.Errorf("Line(0) = %q, want %q", line, "bar")
	}
	if line := m2.buf.Line(1); line != "foo" {
		t.Errorf("Line(1) = %q, want %q", line, "foo")
	}
	if m2.buf.LineCount() != 2 {
		t.Errorf("LineCount() = %d, want 2", m2.buf.LineCount())
	}
}

func TestExecuteMoveLineDownMovesWholeSelection(t *testing.T) {
	m := newTestModel("foo\nbar\nbaz\nqux\n")
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
		IsLine: true,
	}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "baz" {
		t.Errorf("Line(0) = %q, want %q", line, "baz")
	}
	if line := m2.buf.Line(1); line != "foo" {
		t.Errorf("Line(1) = %q, want %q", line, "foo")
	}
	if line := m2.buf.Line(2); line != "bar" {
		t.Errorf("Line(2) = %q, want %q", line, "bar")
	}
	if line := m2.buf.Line(3); line != "qux" {
		t.Errorf("Line(3) = %q, want %q", line, "qux")
	}
	if m2.sel == nil || m2.sel.Anchor.Line != 1 || m2.sel.Head.Line != 2 {
		t.Errorf("sel = %+v, want Anchor.Line=1 Head.Line=2", m2.sel)
	}
}

// TestExecuteMoveLineDownIndentsAfterOpenBrace is a regression test: a line
// moved down to land right after an opener ("{", "(", "[", or a trailing
// ':') should pick up one more level of indent, the same rule handleEnter
// uses for a freshly typed line there.
func TestExecuteMoveLineDownIndentsAfterOpenBrace(t *testing.T) {
	m := newTestModel("foo\nif x {\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "if x {" {
		t.Errorf("Line(0) = %q, want %q", line, "if x {")
	}
	if line := m2.buf.Line(1); line != "\tfoo" {
		t.Errorf("Line(1) = %q, want %q (indented one level after the open brace)", line, "\tfoo")
	}
	if line := m2.buf.Line(2); line != "bar" {
		t.Errorf("Line(2) = %q, want %q", line, "bar")
	}
}

// TestExecuteMoveLineUpDedentsWhenLeavingBraceBlock is a regression test:
// moving the reverse direction (out from under an opener) should dedent
// back, settling on each press rather than only once the user stops.
func TestExecuteMoveLineUpDedentsWhenLeavingBraceBlock(t *testing.T) {
	m := newTestModel("if x {\n\tfoo\nbar\n")
	m.cursor = document.Pos{Line: 1, Col: 1}

	got, _ := executeMoveLineUp(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo" {
		t.Errorf("Line(0) = %q, want %q (dedented after leaving the brace block)", line, "foo")
	}
	if line := m2.buf.Line(1); line != "if x {" {
		t.Errorf("Line(1) = %q, want %q", line, "if x {")
	}
}

// TestExecuteMoveLineDownPreservesRelativeIndentWithinBlock is a
// regression test: reindenting a multi-line selection shifts every line by
// the same amount rather than flattening it, so internal nesting survives
// the move.
func TestExecuteMoveLineDownPreservesRelativeIndentWithinBlock(t *testing.T) {
	m := newTestModel("foo\n\tbar\nif x {\nqux\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
		IsLine: true,
	}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "if x {" {
		t.Errorf("Line(0) = %q, want %q", line, "if x {")
	}
	if line := m2.buf.Line(1); line != "\tfoo" {
		t.Errorf("Line(1) = %q, want %q", line, "\tfoo")
	}
	if line := m2.buf.Line(2); line != "\t\tbar" {
		t.Errorf("Line(2) = %q, want %q (nesting relative to foo preserved)", line, "\t\tbar")
	}
}

// TestExecuteMoveLineDownDedentsOneLevelPastNestedBlock is a regression
// test: moving a statement down past a nested block's closer, so it lands
// as the last statement in the *enclosing* function body (sibling to the
// nested block, not inside it), must dedent it to match that body level —
// not all the way to the function's own declaration indent, which an
// earlier, overly broad use of DedentTarget produced (it looked at
// whether the line the block would land *before* opened with a closer,
// which fires even when a sibling statement right above already
// establishes the correct body indent).
func TestExecuteMoveLineDownDedentsOneLevelPastNestedBlock(t *testing.T) {
	m := newAutoPairGoTestModel(t, "func foo() {\n\tif x {\n\t\tbar()\n\t}\n}\n")
	m.cursor = document.Pos{Line: 2, Col: 2}

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(2); line != "\t}" {
		t.Errorf("Line(2) = %q, want %q", line, "\t}")
	}
	if line := m2.buf.Line(3); line != "\tbar()" {
		t.Errorf("Line(3) = %q, want %q (one level, matching the function body — not \\t\\t, and not fully dedented to \"\")", line, "\tbar()")
	}
}

// TestExecuteMoveLineUpIndentsImmediatelyWithSiblingInBlock is a
// regression test for the exact bug reported against this feature: moving
// a multi-line selection up into a brace block that already contains an
// indented sibling statement must indent it on the very first press (to
// match that sibling), not stay flat until the selection is dragged all
// the way up past the sibling too.
func TestExecuteMoveLineUpIndentsImmediatelyWithSiblingInBlock(t *testing.T) {
	m := newTestModel("if x {\n\ty := 1\n}\nnew1\nnew2\n")
	m.cursor = document.Pos{Line: 3, Col: 0}
	m.sel = &Selection{
		Anchor: document.Pos{Line: 3, Col: 0},
		Head:   document.Pos{Line: 4, Col: 0},
		IsLine: true,
	}

	got, _ := executeMoveLineUp(m)
	m2 := got.(Model)

	if line := m2.buf.Line(2); line != "\tnew1" {
		t.Errorf("Line(2) = %q, want %q (indented on the first press, matching the sibling y := 1)", line, "\tnew1")
	}
	if line := m2.buf.Line(3); line != "\tnew2" {
		t.Errorf("Line(3) = %q, want %q", line, "\tnew2")
	}
	if line := m2.buf.Line(4); line != "}" {
		t.Errorf("Line(4) = %q, want %q", line, "}")
	}
}

// TestExecuteMoveLineDownAdjustsCursorColumn is a regression test: when moving
// a line down causes reindentation, the cursor column must shift by the same
// delta so it stays on the same character.
func TestExecuteMoveLineDownAdjustsCursorColumn(t *testing.T) {
	m := newTestModel("foo\nif x {\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 2} // on the second 'o' of "foo"

	got, _ := executeMoveLineDown(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "if x {" {
		t.Errorf("Line(0) = %q, want %q", line, "if x {")
	}
	if line := m2.buf.Line(1); line != "\tfoo" {
		t.Errorf("Line(1) = %q, want %q (indented after the brace)", line, "\tfoo")
	}
	// Cursor was at column 2 on "foo", now at column 2+1=3 on "\tfoo" (the same 'o').
	if m2.cursor != (document.Pos{Line: 1, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:3} (adjusted for the added tab)", m2.cursor)
	}
}

// TestExecuteMoveLineUpAdjustsSelectionColumns is a regression test: when
// moving a selection up causes dedentation, both anchor and head columns must
// shift by the same delta.
func TestExecuteMoveLineUpAdjustsSelectionColumns(t *testing.T) {
	m := newTestModel("if x {\n\tfoo\n\tbar\nqux\n")
	m.cursor = document.Pos{Line: 2, Col: 4} // on 'r' in "\tbar"
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 2}, // on 'o' in "\tfoo"
		Head:   document.Pos{Line: 2, Col: 4}, // on 'r' in "\tbar"
		IsLine: true,
	}

	got, _ := executeMoveLineUp(m)
	m2 := got.(Model)

	if line := m2.buf.Line(0); line != "foo" {
		t.Errorf("Line(0) = %q, want %q (dedented after leaving the brace block)", line, "foo")
	}
	if line := m2.buf.Line(1); line != "bar" {
		t.Errorf("Line(1) = %q, want %q (dedented after leaving the brace block)", line, "bar")
	}
	if line := m2.buf.Line(2); line != "if x {" {
		t.Errorf("Line(2) = %q, want %q", line, "if x {")
	}
	// Selection anchor was at column 2 on "\tfoo", now at column 2-1=1 on "foo".
	// Selection head was at column 4 on "\tbar", now at column 4-1=3 on "bar".
	if m2.sel == nil {
		t.Fatal("sel is nil, want non-nil")
	}
	if m2.sel.Anchor != (document.Pos{Line: 0, Col: 1}) {
		t.Errorf("sel.Anchor = %+v, want {Line:0 Col:1} (adjusted for removed tab)", m2.sel.Anchor)
	}
	if m2.sel.Head != (document.Pos{Line: 1, Col: 3}) {
		t.Errorf("sel.Head = %+v, want {Line:1 Col:3} (adjusted for removed tab)", m2.sel.Head)
	}
	// Cursor follows head.
	if m2.cursor != (document.Pos{Line: 1, Col: 3}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:3} (follows head)", m2.cursor)
	}
}
