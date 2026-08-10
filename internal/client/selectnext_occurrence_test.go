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
