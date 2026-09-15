package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestSearchMatchesRederivedAfterRemoteEdit reproduces the two-window report:
// search for "main" in one window, insert a character into it from the other,
// and the highlight must stop claiming a match that is no longer there.
//
// Shifting the stored positions would not be enough. The matched *text* changed,
// so the only correct answer is to re-derive the matches from the buffer.
func TestSearchMatchesRederivedAfterRemoteEdit(t *testing.T) {
	m := newTestModel("func main() {\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.searchQuery = "main"
	m.updateSearch()

	if len(m.searchMatches) != 1 || m.searchMatches[0].col != 5 {
		t.Fatalf("test setup: matches = %+v, want one at column 5", m.searchMatches)
	}

	// The other window turns "main" into "maain".
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 7, InsertText: "a"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if got := m2.buf.Content(); got != "func maain() {\n" {
		t.Fatalf("content = %q, want %q", got, "func maain() {\n")
	}
	if len(m2.searchMatches) != 0 {
		t.Errorf("still reporting %d match(es) for %q in %q — the highlighted text no longer matches",
			len(m2.searchMatches), m2.searchQuery, m2.buf.Content())
	}
}

// TestSearchMatchesFollowRemoteEditElsewhere: an edit that does not break the
// match must leave it matched, at its new position.
func TestSearchMatchesFollowRemoteEditElsewhere(t *testing.T) {
	m := newTestModel("func main() {\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.searchQuery = "main"
	m.updateSearch()

	// Insert before the match; it should still match, four columns further on.
	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "XXXX"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if len(m2.searchMatches) != 1 {
		t.Fatalf("matches = %+v, want one", m2.searchMatches)
	}
	if got := m2.searchMatches[0].col; got != 9 {
		t.Errorf("match column = %d, want 9 — four runes were inserted before it", got)
	}
}

// TestSearchMatchesGainedFromRemoteEdit: an edit that creates a new occurrence
// must produce a match, which stale results can never do.
func TestSearchMatchesGainedFromRemoteEdit(t *testing.T) {
	m := newTestModel("func main() {\n")
	m.rpc = &RPC{}
	m.generation = 1
	m.generationKnown = true
	m.searchQuery = "main"
	m.updateSearch()

	remote := document.Op{Version: 1, Type: document.OpInsert, InsertLine: 0, InsertCol: 13, InsertText: " main"}
	updated, _ := m.Update(updatesMsg{bufID: m.bufID, ops: []document.Op{remote}, version: 1, generation: 1})
	m2 := updated.(Model)

	if len(m2.searchMatches) != 2 {
		t.Errorf("matches = %d, want 2 after the other window added another occurrence", len(m2.searchMatches))
	}
}
