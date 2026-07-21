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
