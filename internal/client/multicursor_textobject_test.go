package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

// resolveMi runs an mi/ma entry the way the keymap does, through the
// perCursor wrapper, so the test exercises what the key actually invokes.
func resolveMi(t *testing.T, key string, m Model) Model {
	t.Helper()
	for _, c := range commandMenuChildrenFor(t, "m", "i") {
		if c.key == key {
			out, _ := c.execute(m)
			return out.(Model)
		}
	}
	t.Fatalf("no mi %q entry", key)
	return m
}

// commandMenuChildrenFor walks the keypress tree to a submenu's children.
func commandMenuChildrenFor(t *testing.T, path ...string) []command {
	t.Helper()
	nodes := prefixCmds
	var cur *command
	for _, k := range path {
		found := false
		for i := range nodes {
			if nodes[i].key == k {
				cur = &nodes[i]
				nodes = nodes[i].children
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("no node %q", k)
		}
	}
	_ = cur
	return nodes
}

// Two cursors inside the same word select it once, not twice. N identical
// selections would make a following d delete the range and then delete
// whatever moved into those coordinates.
func TestTextObjectCollapsesCursorsOnOneObject(t *testing.T) {
	m := newTestModel("alpha beta\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 0}                                          // in "alpha"
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 0, Col: 3}, goalCol: -1}} // also in "alpha"

	got := resolveMi(t, "w", m)

	if len(got.extraCursors) != 0 {
		t.Errorf("got %d extra cursors, want 0 — both resolved to the same word", len(got.extraCursors))
	}
	if got.sel == nil || got.sel.Anchor.Col != 0 || got.sel.Head.Col != 4 {
		t.Errorf("selection = %+v, want alpha (cols 0..4)", got.sel)
	}
}

// Cursors in different words still produce one selection each.
func TestTextObjectKeepsCursorsOnDistinctObjects(t *testing.T) {
	m := newTestModel("alpha beta\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 0}                                          // "alpha"
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 0, Col: 7}, goalCol: -1}} // "beta"

	got := resolveMi(t, "w", m)

	if len(got.extraCursors) != 1 {
		t.Fatalf("got %d extra cursors, want 1 — distinct words must stay distinct", len(got.extraCursors))
	}
	if got.extraCursors[0].sel == nil {
		t.Fatal("extra cursor has no selection")
	}
	if got.extraCursors[0].sel.Anchor.Col != 6 || got.extraCursors[0].sel.Head.Col != 9 {
		t.Errorf("extra selection = %+v, want beta (cols 6..9)", got.extraCursors[0].sel)
	}
}

// dedupeCursors keys a selection by its range, so a flipped selection over the
// same text is still a duplicate.
func TestDedupeCursorsTreatsAFlippedSelectionAsDuplicate(t *testing.T) {
	m := newTestModel("abcdef\n")
	m.cursor = document.Pos{Line: 0, Col: 4}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 1}, Head: document.Pos{Line: 0, Col: 4}}
	m.extraCursors = []ExtraCursor{{
		pos: document.Pos{Line: 0, Col: 1},
		sel: &Selection{Anchor: document.Pos{Line: 0, Col: 4}, Head: document.Pos{Line: 0, Col: 1}},
	}}

	m.dedupeCursors(dedupeBySelection)

	if len(m.extraCursors) != 0 {
		t.Error("a selection over the same range with anchor/head swapped was kept as distinct")
	}
}

// Cursors with no selection dedupe on position.
func TestDedupeCursorsCollapsesCoincidentBareCursors(t *testing.T) {
	m := newTestModel("abc\ndef\n")
	m.cursor = document.Pos{Line: 1, Col: 2}
	m.extraCursors = []ExtraCursor{
		{pos: document.Pos{Line: 1, Col: 2}}, // same as primary
		{pos: document.Pos{Line: 0, Col: 0}}, // distinct
	}

	m.dedupeCursors(dedupeBySelection)

	if len(m.extraCursors) != 1 || m.extraCursors[0].pos.Line != 0 {
		t.Errorf("extraCursors = %+v, want only the distinct one at line 0", m.extraCursors)
	}
}

// mi f (select inside function) must now apply at every cursor — the case the
// report was about. Two cursors in two different functions get one selection
// each; before this they produced one, on the primary cursor only.
//
// Needs a real tree-sitter Go grammar, so it skips on a build without one
// (make build-minimal / plain `go test` with no lang tag).
func TestTreeSitterTextObjectAppliesToAllCursors(t *testing.T) {
	src := "package p\n\nfunc one() {\n\ta := 1\n}\n\nfunc two() {\n\tb := 2\n}\n"
	m := newTestModel(src)
	m.rpc = &RPC{}
	m.hlr = highlight.New("test.go")
	if m.hlr == nil {
		t.Skip("no Go highlighter in this build")
	}
	m.cursor = document.Pos{Line: 3, Col: 2}                                          // inside one()
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 7, Col: 2}, goalCol: -1}} // inside two()

	got := resolveMi(t, "f", m)

	if got.sel == nil {
		t.Fatal("primary cursor has no selection")
	}
	if len(got.extraCursors) != 1 {
		t.Fatalf("got %d extra cursors, want 1", len(got.extraCursors))
	}
	if got.extraCursors[0].sel == nil {
		t.Fatal("extra cursor has no selection: mi f reached the primary cursor only")
	}
	// Each selection must cover its own function, not the same one twice.
	if got.sel.Anchor.Line == got.extraCursors[0].sel.Anchor.Line {
		t.Errorf("both selections start at line %d; each cursor should have selected its own function",
			got.sel.Anchor.Line)
	}
}

// Two cursors inside the *same* function collapse to one selection.
func TestTreeSitterTextObjectCollapsesCursorsInOneFunction(t *testing.T) {
	src := "package p\n\nfunc one() {\n\ta := 1\n\tb := 2\n}\n"
	m := newTestModel(src)
	m.rpc = &RPC{}
	m.hlr = highlight.New("test.go")
	if m.hlr == nil {
		t.Skip("no Go highlighter in this build")
	}
	m.cursor = document.Pos{Line: 3, Col: 2}
	m.extraCursors = []ExtraCursor{{pos: document.Pos{Line: 4, Col: 2}, goalCol: -1}}

	got := resolveMi(t, "f", m)

	if len(got.extraCursors) != 0 {
		t.Errorf("got %d extra cursors, want 0 — both are in the same function", len(got.extraCursors))
	}
	if got.sel == nil {
		t.Fatal("no selection left after collapsing")
	}
}
