package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestSortLinesAscending verifies the selected line range is replaced with
// the same lines sorted lexicographically ascending, and the trailing
// newline / non-selected lines are left untouched.
func TestSortLinesAscending(t *testing.T) {
	m := newTestModel("banana\napple\ncherry\nkept\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 2, Col: 0},
	}

	m2, _ := sortLines(m, false)

	want := "apple\nbanana\ncherry\nkept\n"
	if got := m2.buf.Content(); got != want {
		t.Errorf("Content() = %q, want %q", got, want)
	}
	if m2.sel == nil {
		t.Fatal("sel = nil, want the sorted lines still selected")
	}
	wantSel := Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 2, Col: max(0, len("cherry")-1)},
		IsLine: true,
	}
	if *m2.sel != wantSel {
		t.Errorf("sel = %+v, want %+v", *m2.sel, wantSel)
	}
	if m2.cursor != m2.sel.Head {
		t.Errorf("cursor = %+v, want sel.Head %+v", m2.cursor, m2.sel.Head)
	}
}

// TestSortLinesReversedSelectionPreservesDirection verifies that when the
// original selection was made "upward" (Anchor below Head, e.g. the user
// selected by moving the cursor up from the last line), sorting keeps the
// selection anchored at the bottom afterward instead of always normalizing
// to anchor-at-top — matching how moveLines/executeIndent preserve an
// existing selection's direction rather than re-deriving a fixed one.
func TestSortLinesReversedSelectionPreservesDirection(t *testing.T) {
	m := newTestModel("banana\napple\ncherry\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 2, Col: 0},
		Head:   document.Pos{Line: 0, Col: 0},
	}

	m2, _ := sortLines(m, false)

	want := "apple\nbanana\ncherry\n"
	if got := m2.buf.Content(); got != want {
		t.Errorf("Content() = %q, want %q", got, want)
	}
	if m2.sel == nil {
		t.Fatal("sel = nil, want the sorted lines still selected")
	}
	wantSel := Selection{
		Anchor: document.Pos{Line: 2, Col: max(0, len("cherry")-1)},
		Head:   document.Pos{Line: 0, Col: 0},
		IsLine: true,
	}
	if *m2.sel != wantSel {
		t.Errorf("sel = %+v, want %+v (anchor stays at the bottom line)", *m2.sel, wantSel)
	}
	if m2.cursor != m2.sel.Head {
		t.Errorf("cursor = %+v, want sel.Head %+v", m2.cursor, m2.sel.Head)
	}
}

// TestSortLinesDescending verifies descending order.
func TestSortLinesDescending(t *testing.T) {
	m := newTestModel("banana\napple\ncherry\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 2, Col: 0},
	}

	m2, _ := sortLines(m, true)

	want := "cherry\nbanana\napple\n"
	if got := m2.buf.Content(); got != want {
		t.Errorf("Content() = %q, want %q", got, want)
	}
	if m2.sel == nil || m2.sel.Anchor.Line != 0 || m2.sel.Head.Line != 2 {
		t.Errorf("sel = %+v, want a selection still spanning lines 0-2", m2.sel)
	}
}

// TestSortLinesNoSelectionIsNoOp verifies sorting with no multi-line
// selection (just the cursor on a single line) leaves the buffer untouched,
// since there's nothing to reorder.
func TestSortLinesNoSelectionIsNoOp(t *testing.T) {
	m := newTestModel("banana\napple\ncherry\n")
	m.cursor = document.Pos{Line: 1, Col: 0}

	m2, cmd := sortLines(m, false)

	if got := m2.buf.Content(); got != "banana\napple\ncherry\n" {
		t.Errorf("Content() = %q, want unchanged", got)
	}
	if cmd != nil {
		t.Errorf("cmd = %v, want nil", cmd)
	}
}

// TestSortLinesLastLineNoTrailingNewline verifies sorting a range that
// includes the buffer's final line, when the buffer has no trailing "\n",
// doesn't introduce one (mirrors moveLines' handling of the same case).
func TestSortLinesLastLineNoTrailingNewline(t *testing.T) {
	m := newTestModel("banana\napple\ncherry")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 2, Col: 0},
	}

	m2, _ := sortLines(m, false)

	want := "apple\nbanana\ncherry"
	if got := m2.buf.Content(); got != want {
		t.Errorf("Content() = %q, want %q", got, want)
	}
}
