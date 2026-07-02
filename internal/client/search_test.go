package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func TestIsSmartCaseSensitive(t *testing.T) {
	cases := []struct{ pat string; want bool }{
		{"hello", false},
		{"Hello", true},
		{"WORLD", true},
		{"", false},
		{"foo123", false},
		{"Foo123", true},
	}
	for _, c := range cases {
		if got := isSmartCaseSensitive(c.pat); got != c.want {
			t.Errorf("isSmartCaseSensitive(%q) = %v, want %v", c.pat, got, c.want)
		}
	}
}

func TestFindMatchesBasic(t *testing.T) {
	buf := document.New("", "hello world\nhello again\n")
	matches := findMatches(buf, "hello")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if matches[0].line != 0 || matches[0].col != 0 {
		t.Errorf("match[0]: got line=%d col=%d, want 0,0", matches[0].line, matches[0].col)
	}
	if matches[1].line != 1 || matches[1].col != 0 {
		t.Errorf("match[1]: got line=%d col=%d, want 1,0", matches[1].line, matches[1].col)
	}
}

func TestFindMatchesCaseInsensitive(t *testing.T) {
	buf := document.New("", "Hello World\n")
	matches := findMatches(buf, "hello")
	if len(matches) != 1 {
		t.Fatalf("case-insensitive: expected 1 match, got %d", len(matches))
	}
}

func TestFindMatchesCaseSensitive(t *testing.T) {
	buf := document.New("", "Hello hello\n")
	matches := findMatches(buf, "Hello")
	if len(matches) != 1 {
		t.Fatalf("case-sensitive: expected 1 match, got %d", len(matches))
	}
	if matches[0].col != 0 {
		t.Errorf("case-sensitive: expected col 0, got %d", matches[0].col)
	}
}

func TestFindMatchesEmpty(t *testing.T) {
	buf := document.New("", "hello\n")
	if matches := findMatches(buf, ""); matches != nil {
		t.Errorf("empty pattern: expected nil, got %v", matches)
	}
}

func TestFindMatchesNone(t *testing.T) {
	buf := document.New("", "hello\n")
	if matches := findMatches(buf, "xyz"); len(matches) != 0 {
		t.Errorf("no matches: expected 0, got %d", len(matches))
	}
}

func TestMatchIdxAtOrAfter(t *testing.T) {
	matches := []searchMatch{
		{line: 0, col: 5},
		{line: 2, col: 0},
		{line: 4, col: 3},
	}
	// Exact match
	if i := matchIdxAtOrAfter(matches, 2, 0); i != 1 {
		t.Errorf("exact: got %d, want 1", i)
	}
	// After end → wraps to 0
	if i := matchIdxAtOrAfter(matches, 10, 0); i != 0 {
		t.Errorf("wrap: got %d, want 0", i)
	}
	// Before first
	if i := matchIdxAtOrAfter(matches, 0, 0); i != 0 {
		t.Errorf("before first: got %d, want 0", i)
	}
}

func TestMatchIdxAtOrAfterEmpty(t *testing.T) {
	if i := matchIdxAtOrAfter(nil, 0, 0); i != -1 {
		t.Errorf("empty: got %d, want -1", i)
	}
}

// --- integration: entering search mode and navigating ---

func TestSearchModeEnterAndCancel(t *testing.T) {
	m := newTestModel("hello world\nhello again\n")
	m.cursor.Line = 0
	m.cursor.Col = 0

	// Press "/" to enter search mode.
	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	if got.mode != ModeSearch {
		t.Fatalf("after '/': mode = %v, want ModeSearch", got.mode)
	}

	// Type "hello".
	for _, ch := range "hello" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	if got.searchQuery != "hello" {
		t.Errorf("searchQuery = %q, want 'hello'", got.searchQuery)
	}
	if len(got.searchMatches) != 2 {
		t.Errorf("searchMatches = %d, want 2", len(got.searchMatches))
	}

	// Esc cancels and restores cursor.
	origin := got.searchOrigin
	m2, _ = got.handleKey(fakeKey("esc"))
	got = m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("after esc: mode = %v, want ModeNormal", got.mode)
	}
	if got.cursor != origin {
		t.Errorf("cursor not restored: got %v, want %v", got.cursor, origin)
	}
	if got.searchQuery != "" {
		t.Errorf("searchQuery not cleared after esc")
	}
}

func TestSearchModeEnterConfirm(t *testing.T) {
	m := newTestModel("hello world\nhello again\n")

	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	for _, ch := range "hello" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	// Enter confirms.
	m2, _ = got.handleKey(fakeKey("enter"))
	got = m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("after enter: mode = %v, want ModeNormal", got.mode)
	}
	// Cursor should be on a match.
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("cursor after confirm: got %v, want {0,0}", got.cursor)
	}
}

func TestSearchNavigation(t *testing.T) {
	m := newTestModel("aaa bbb\naaa ccc\n")
	// Prime matches by going through search mode.
	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	for _, ch := range "aaa" {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	m2, _ = got.handleKey(fakeKey("enter"))
	got = m2.(Model)
	// Should be on first match.
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("first match: got %v, want {0,0}", got.cursor)
	}

	// n → next match.
	m2, _ = got.handleKey(fakeKey("n"))
	got = m2.(Model)
	if got.cursor.Line != 1 || got.cursor.Col != 0 {
		t.Errorf("n: got %v, want {1,0}", got.cursor)
	}

	// N → previous match.
	m2, _ = got.handleKey(fakeKey("N"))
	got = m2.(Model)
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("N: got %v, want {0,0}", got.cursor)
	}
}
