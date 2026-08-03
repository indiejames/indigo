package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

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
