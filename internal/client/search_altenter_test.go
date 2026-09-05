package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// TestHandleSearchAltEnterSelectsAllMatches is a regression/feature test for
// alt+enter in ModeSearch: it should convert every current match into a
// cursor (primary + extraCursors), exit search mode, and clear the search
// query/matches so the search overlay doesn't linger over the new selections.
func TestHandleSearchAltEnterSelectsAllMatches(t *testing.T) {
	m := newTestModel("foo bar foo baz foo\n")
	m.mode = ModeSearch
	m.searchQuery = "foo"
	m.updateSearch()
	if len(m.searchMatches) != 3 {
		t.Fatalf("updateSearch found %d matches, want 3", len(m.searchMatches))
	}

	res, cmd := m.handleSearch(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	if cmd != nil {
		t.Errorf("handleSearch(alt+enter) returned a non-nil cmd, want nil")
	}
	m2 := res.(Model)

	if m2.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", m2.mode)
	}
	if m2.searchQuery != "" || len(m2.searchMatches) != 0 {
		t.Errorf("search state not cleared: searchQuery=%q searchMatches=%v", m2.searchQuery, m2.searchMatches)
	}
	if m2.sel == nil {
		t.Fatal("m2.sel is nil, want a selection covering the first match")
	}
	if len(m2.extraCursors) != 2 {
		t.Fatalf("len(extraCursors) = %d, want 2", len(m2.extraCursors))
	}
	if m2.sel.Anchor != (document.Pos{Line: 0, Col: 0}) {
		t.Errorf("primary selection anchor = %+v, want (0,0)", m2.sel.Anchor)
	}
}

// TestHandleSearchAltEnterNoMatchesShowsError verifies alt+enter with no
// matches (e.g. an empty query, or a pattern found nothing) leaves the user
// in search mode with an error, instead of silently exiting to Normal mode
// with no selection.
func TestHandleSearchAltEnterNoMatchesShowsError(t *testing.T) {
	m := newTestModel("foo bar\n")
	m.mode = ModeSearch
	m.searchQuery = "nope"
	m.updateSearch()
	if len(m.searchMatches) != 0 {
		t.Fatalf("updateSearch found %d matches, want 0", len(m.searchMatches))
	}

	res, _ := m.handleSearch(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
	m2 := res.(Model)

	if m2.mode != ModeSearch {
		t.Errorf("mode = %v, want ModeSearch (should stay put on no matches)", m2.mode)
	}
	if m2.sel != nil || len(m2.extraCursors) != 0 {
		t.Error("selection/extraCursors mutated despite no matches")
	}
	if m2.status == "" {
		t.Error("expected a status message on no-match alt+enter")
	}
}
