package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestSelectNextOccurrenceWrapDoesNotDuplicatePrimary is a regression test:
// once every occurrence of the selected word has a cursor, wrapping around
// on a further Ctrl+D used to land back on the primary selection's own
// position and add a duplicate ExtraCursor there.
func TestSelectNextOccurrenceWrapDoesNotDuplicatePrimary(t *testing.T) {
	m := newTestModel("foo bar foo\n")
	m.cursor = document.Pos{Line: 0, Col: 0}

	if !selectNextOccurrence(&m) { // selects first "foo" (word at cursor)
		t.Fatal("initial word selection failed")
	}
	if !selectNextOccurrence(&m) { // adds cursor on second "foo"
		t.Fatal("expected a second occurrence to be found")
	}
	if len(m.extraCursors) != 1 {
		t.Fatalf("extraCursors = %d, want 1 after selecting the second occurrence", len(m.extraCursors))
	}

	// A third call has nothing left to select: the only remaining match is
	// the wrap-around hit on the primary's own position.
	changed := selectNextOccurrence(&m)
	if changed {
		t.Error("selectNextOccurrence returned true when wrapping back to the primary selection — should be a no-op")
	}
	if len(m.extraCursors) != 1 {
		t.Errorf("extraCursors = %d, want still 1 (no duplicate added)", len(m.extraCursors))
	}
}

// TestSelectNextOccurrenceNoDuplicateAfterOutOfOrderWrap covers the subtler
// case: the primary selection isn't the first occurrence in the buffer, so
// a wrap adds a cursor *earlier* in the buffer than a previously-added one.
// A later plain forward search (not itself a wrap) can then re-find an
// already-selected occurrence; that must also be rejected, not just the
// wrap-triggered find.
func TestSelectNextOccurrenceNoDuplicateAfterOutOfOrderWrap(t *testing.T) {
	m := newTestModel("foo AAA foo BBB foo\n")
	m.cursor = document.Pos{Line: 0, Col: 8} // the middle "foo"

	if !selectNextOccurrence(&m) { // primary selects the middle "foo"
		t.Fatal("initial word selection failed")
	}
	if !selectNextOccurrence(&m) { // forward: selects the third "foo" (col 16)
		t.Fatal("expected the third occurrence to be found")
	}
	if !selectNextOccurrence(&m) { // wraps: selects the first "foo" (col 0) — genuinely new
		t.Fatal("expected the wrap-around to find the first occurrence (not yet selected)")
	}
	if len(m.extraCursors) != 2 {
		t.Fatalf("extraCursors = %d, want 2 (third and first occurrences)", len(m.extraCursors))
	}

	// All three occurrences are now covered (primary=middle, extras=third,first).
	// The next search would otherwise re-find the middle occurrence (the
	// primary) via a plain forward scan, since the most-recently-added
	// cursor (first "foo", col 0) is earlier in the buffer than the primary.
	changed := selectNextOccurrence(&m)
	if changed {
		t.Error("selectNextOccurrence returned true for an already-fully-covered set of occurrences")
	}
	if len(m.extraCursors) != 2 {
		t.Errorf("extraCursors = %d, want still 2 (no duplicate added)", len(m.extraCursors))
	}
}

// TestSelectNextOccurrenceSeedsFromSearchMatch verifies that when the
// cursor is sitting on the active / search match (as it is right after
// committing a search with Enter, or after n/N), Ctrl+D selects that exact
// match instead of falling back to the word-boundary heuristic — important
// for non-word matches (e.g. regex) where the two would disagree.
func TestSelectNextOccurrenceSeedsFromSearchMatch(t *testing.T) {
	m := newTestModel("foo.bar foo.bar foo.bar\n")
	matches, err := findMatches(m.buf, "foo.bar")
	if err != nil {
		t.Fatalf("findMatches: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("findMatches found %d matches, want 3", len(matches))
	}
	m.searchMatches = matches
	m.searchIdx = 0
	m.cursor = document.Pos{Line: 0, Col: matches[0].col}

	if !selectNextOccurrence(&m) {
		t.Fatal("selectNextOccurrence returned false seeding from a search match")
	}
	wantSel := &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 0, Col: 6},
	}
	if m.sel == nil || *m.sel != *wantSel {
		t.Errorf("m.sel = %+v, want %+v (the full search match, not a word-boundary guess)", m.sel, wantSel)
	}
	if m.cursor != wantSel.Head {
		t.Errorf("cursor = %+v, want %+v", m.cursor, wantSel.Head)
	}
	// Search state should be cleared, same as Alt+Enter — Ctrl+D now drives.
	if len(m.searchMatches) != 0 || m.searchIdx != -1 {
		t.Errorf("search state not cleared: searchMatches=%v searchIdx=%d", m.searchMatches, m.searchIdx)
	}

	// A second Ctrl+D should behave like the normal incremental flow and
	// pick up the next occurrence.
	if !selectNextOccurrence(&m) {
		t.Fatal("expected a second occurrence to be found")
	}
	if len(m.extraCursors) != 1 {
		t.Fatalf("extraCursors = %d, want 1", len(m.extraCursors))
	}
	if m.extraCursors[0].sel.Anchor.Col != 8 {
		t.Errorf("extraCursors[0] anchor col = %d, want 8 (second 'foo.bar')", m.extraCursors[0].sel.Anchor.Col)
	}
}

// TestSelectNextOccurrenceIgnoresStaleSearchMatchAfterCursorMoves verifies
// the search-seed path only kicks in while the cursor is still exactly on
// the active match — once the user has moved elsewhere (e.g. with h/j/k/l),
// Ctrl+D must fall back to the ordinary word-boundary selection, not reuse
// a now-unrelated search.
func TestSelectNextOccurrenceIgnoresStaleSearchMatchAfterCursorMoves(t *testing.T) {
	m := newTestModel("foo.bar baz\n")
	matches, err := findMatches(m.buf, "foo.bar")
	if err != nil {
		t.Fatalf("findMatches: %v", err)
	}
	m.searchMatches = matches
	m.searchIdx = 0
	// Cursor has moved off the match (onto "baz").
	m.cursor = document.Pos{Line: 0, Col: 8}

	if !selectNextOccurrence(&m) {
		t.Fatal("selectNextOccurrence returned false")
	}
	if m.sel == nil || m.sel.Anchor.Col != 8 || m.sel.Head.Col != 10 {
		t.Errorf("m.sel = %+v, want the word 'baz' (col 8-10), not the stale search match", m.sel)
	}
	// Stale search state is untouched by the word-boundary path.
	if len(m.searchMatches) == 0 {
		t.Error("search state should be left alone when the seed path doesn't fire")
	}
}

// TestSelectNextOccurrenceIgnoresZeroLengthSearchMatch verifies a
// zero-width search match (possible with a regex like \b) at the cursor
// doesn't seed Ctrl+D — there's no text to build a multi-cursor selection
// from — and falls back to the word-boundary heuristic instead.
func TestSelectNextOccurrenceIgnoresZeroLengthSearchMatch(t *testing.T) {
	m := newTestModel("foo\n")
	m.searchMatches = []substituteMatch{{line: 0, col: 0, length: 0}}
	m.searchIdx = 0
	m.cursor = document.Pos{Line: 0, Col: 0}

	if !selectNextOccurrence(&m) {
		t.Fatal("selectNextOccurrence returned false")
	}
	if m.sel == nil || m.sel.Anchor.Col != 0 || m.sel.Head.Col != 2 {
		t.Errorf("m.sel = %+v, want the whole word 'foo' (col 0-2) via the fallback path", m.sel)
	}
}
