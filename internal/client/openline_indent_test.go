package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// executeOpenLineBelow/Above insert a fresh line the same way Enter does
// (via contextIndent), so they should agree with handleEnter on identical
// buffer states rather than always landing at column 0 the way a bare "\n"
// insert would.

func TestOpenLineBelowIndentsAfterOpenBrace(t *testing.T) {
	m := newAutoPairTestModel("if x {\n}\n")
	m.mode = ModeNormal
	m.cursor = document.Pos{Line: 0, Col: len("if x {")}

	m2, _ := executeOpenLineBelow(m)
	got := m2.(Model)

	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q (one level deeper, matching handleEnter)", got.buf.Line(1), "\t")
	}
	if got.cursor != (document.Pos{Line: 1, Col: 1}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:1}", got.cursor)
	}
	if got.mode != ModeInsert {
		t.Errorf("mode = %v, want ModeInsert", got.mode)
	}
}

func TestOpenLineBelowMatchesCurrentLineIndentWithoutOpener(t *testing.T) {
	m := newAutoPairTestModel("\tfoo\n\tbar\n")
	m.mode = ModeNormal
	m.cursor = document.Pos{Line: 0, Col: 1} // anywhere on "\tfoo"

	m2, _ := executeOpenLineBelow(m)
	got := m2.(Model)

	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q (same indent as line 0, no trailing opener)", got.buf.Line(1), "\t")
	}
	if got.buf.Line(0) != "\tfoo" {
		t.Errorf("line 0 = %q, want unchanged %q", got.buf.Line(0), "\tfoo")
	}
}

func TestOpenLineBelowDedentsBeforeCloser(t *testing.T) {
	// Cursor on the "if x {" line itself but opening below when the very
	// next line is the closing brace should still land one level deeper
	// (the new line becomes the block's first statement, same as Enter
	// pressed at the end of "if x {").
	m := newAutoPairGoTestModel(t, "func foo() {\n\tif x {\n\t}\n}\n")
	m.mode = ModeNormal
	m.cursor = document.Pos{Line: 1, Col: 2}

	m2, _ := executeOpenLineBelow(m)
	got := m2.(Model)

	if got.buf.Line(2) != "\t\t" {
		t.Errorf("line 2 = %q, want %q (one level deeper than \"if x {\")", got.buf.Line(2), "\t\t")
	}
}

func TestOpenLineAboveMatchesPrecedingLineIndent(t *testing.T) {
	m := newAutoPairTestModel("if x {\n\tfoo\n}\n")
	m.mode = ModeNormal
	m.cursor = document.Pos{Line: 1, Col: 0} // on "\tfoo"

	m2, _ := executeOpenLineAbove(m)
	got := m2.(Model)

	if got.buf.Line(1) != "\t" {
		t.Errorf("line 1 = %q, want %q (indented one level, matching line above's trailing opener)", got.buf.Line(1), "\t")
	}
	if got.buf.Line(2) != "\tfoo" {
		t.Errorf("line 2 = %q, want unchanged %q", got.buf.Line(2), "\tfoo")
	}
	if got.cursor != (document.Pos{Line: 1, Col: 1}) {
		t.Errorf("cursor = %+v, want {Line:1 Col:1}", got.cursor)
	}
}

func TestOpenLineAboveAtTopOfFileHasNoIndent(t *testing.T) {
	m := newAutoPairTestModel("\tfoo\n")
	m.mode = ModeNormal
	m.cursor = document.Pos{Line: 0, Col: 1}

	m2, _ := executeOpenLineAbove(m)
	got := m2.(Model)

	if got.buf.Line(0) != "" {
		t.Errorf("line 0 = %q, want empty (no line above to inherit indent from)", got.buf.Line(0))
	}
	if got.cursor != (document.Pos{Line: 0, Col: 0}) {
		t.Errorf("cursor = %+v, want {Line:0 Col:0}", got.cursor)
	}
}

// TestOpenLineBelowAgreesWithHandleEnter is a regression test for the bug
// this fixes: 'o' used to always insert a bare "\n" (column 0, no
// indentation) regardless of context, while pressing Enter at the end of
// the same line went through contextIndent. That made the two ways of
// opening a new line below disagree on identical buffer states.
func TestOpenLineBelowAgreesWithHandleEnter(t *testing.T) {
	content := "\tif x {\n\t}\n"

	mEnter := newAutoPairTestModel(content)
	mEnter.cursor = document.Pos{Line: 0, Col: len("\tif x {")}
	viaEnter, _ := mEnter.handleInsert(fakeKey("enter"))
	wantLine := viaEnter.(Model).buf.Line(1)

	mOpen := newAutoPairTestModel(content)
	mOpen.mode = ModeNormal
	mOpen.cursor = document.Pos{Line: 0, Col: 3}
	viaOpen, _ := executeOpenLineBelow(mOpen)
	gotLine := viaOpen.(Model).buf.Line(1)

	if gotLine != wantLine {
		t.Errorf("executeOpenLineBelow produced line 1 = %q, want %q (same as handleEnter)", gotLine, wantLine)
	}
}
