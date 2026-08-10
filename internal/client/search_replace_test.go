package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// typeInSearch drives m through "/" then each rune of query, returning the
// resulting Model still in ModeSearch.
func typeInSearch(t *testing.T, m Model, query string) Model {
	t.Helper()
	m2, _ := m.handleKey(fakeKey("/"))
	got := m2.(Model)
	for _, ch := range query {
		m2, _ = got.handleKey(fakeKey(string(ch)))
		got = m2.(Model)
	}
	return got
}

func TestSearchReplaceEntersPreviewModeOnUnescapedSlash(t *testing.T) {
	m := newTestModel("foo bar\n")
	got := typeInSearch(t, m, "foo/qux")
	if !got.searchReplacing {
		t.Fatal("expected searchReplacing = true after typing an unescaped '/'")
	}
	if got.searchReplace != "qux" {
		t.Errorf("searchReplace = %q, want qux", got.searchReplace)
	}
	if len(got.searchMatches) != 1 || got.searchMatches[0].replacement != "qux" {
		t.Fatalf("expected 1 match with replacement qux, got %+v", got.searchMatches)
	}
	// The buffer itself must not be touched yet — this is preview only.
	if got.buf.Content() != "foo bar\n" {
		t.Errorf("buffer changed during preview: %q", got.buf.Content())
	}
}

func TestSearchReplaceEnterCommits(t *testing.T) {
	m := newTestModel("foo bar foo\n")
	m.rpc = &RPC{}
	got := typeInSearch(t, m, "foo/qux")

	m2, _ := got.handleKey(fakeKey("enter"))
	final := m2.(Model)

	want := "qux bar qux\n"
	if final.buf.Content() != want {
		t.Errorf("content = %q, want %q", final.buf.Content(), want)
	}
	if final.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", final.mode)
	}
	if final.status != "2 substitution(s)" {
		t.Errorf("status = %q, want '2 substitution(s)'", final.status)
	}
	if len(final.messageLog) != 1 || final.messageLog[0].text != "2 substitution(s)" {
		t.Errorf("messageLog = %+v, want a single '2 substitution(s)' entry", final.messageLog)
	}
	// Search state should be fully cleared after committing.
	if final.searchReplacing || final.searchQuery != "" || len(final.searchMatches) != 0 {
		t.Errorf("search state not cleared after commit: %+v", final)
	}
}

func TestSearchReplaceEnterWithoutSlashDoesNotEdit(t *testing.T) {
	// Plain search (no replace delimiter) must behave exactly as before:
	// Enter just confirms the cursor position, no buffer change.
	m := newTestModel("foo bar\n")
	m.rpc = &RPC{}
	got := typeInSearch(t, m, "foo")

	m2, _ := got.handleKey(fakeKey("enter"))
	final := m2.(Model)

	if final.buf.Content() != "foo bar\n" {
		t.Errorf("plain search modified the buffer: %q", final.buf.Content())
	}
	if final.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", final.mode)
	}
}

func TestSearchReplaceEscCancelsWithoutEditing(t *testing.T) {
	m := newTestModel("foo bar foo\n")
	m.rpc = &RPC{}
	got := typeInSearch(t, m, "foo/qux")

	m2, _ := got.handleKey(fakeKey("esc"))
	final := m2.(Model)

	if final.buf.Content() != "foo bar foo\n" {
		t.Errorf("esc should not edit the buffer, got %q", final.buf.Content())
	}
	if final.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", final.mode)
	}
	if final.searchReplacing {
		t.Error("searchReplacing should be cleared after esc")
	}
}

func TestSearchReplaceBackreferences(t *testing.T) {
	m := newTestModel("alice-bob\n")
	m.rpc = &RPC{}
	got := typeInSearch(t, m, `\(\w+)-(\w+)/$2-$1`)

	m2, _ := got.handleKey(fakeKey("enter"))
	final := m2.(Model)

	want := "bob-alice\n"
	if final.buf.Content() != want {
		t.Errorf("content = %q, want %q", final.buf.Content(), want)
	}
}

func TestSearchReplaceScopedToSelection(t *testing.T) {
	m := newTestModel("foo\nfoo\nfoo\n")
	m.rpc = &RPC{}
	// Linewise selection covering just line 1 (0-based).
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 0},
		Head:   document.Pos{Line: 1, Col: 2},
		IsLine: true,
	}
	got := typeInSearch(t, m, "foo/bar")
	if len(got.searchMatches) != 1 || got.searchMatches[0].line != 1 {
		t.Fatalf("expected preview scoped to line 1, got %+v", got.searchMatches)
	}

	m2, _ := got.handleKey(fakeKey("enter"))
	final := m2.(Model)
	want := "foo\nbar\nfoo\n"
	if final.buf.Content() != want {
		t.Errorf("content = %q, want %q", final.buf.Content(), want)
	}
	if final.sel != nil {
		t.Errorf("selection should be cleared after commit, got %+v", final.sel)
	}
}

func TestSearchScopedToSelectionWhenNotReplacing(t *testing.T) {
	// Plain search (no replace) is scoped to the selection too, for
	// consistency with search-and-replace.
	m := newTestModel("foo\nfoo\nfoo\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 1, Col: 0},
		Head:   document.Pos{Line: 1, Col: 2},
		IsLine: true,
	}
	got := typeInSearch(t, m, "foo")
	if len(got.searchMatches) != 1 || got.searchMatches[0].line != 1 {
		t.Fatalf("expected search scoped to line 1, got %+v", got.searchMatches)
	}
}

func TestSearchReplaceNoMatchStatus(t *testing.T) {
	m := newTestModel("hello\n")
	m.rpc = &RPC{}
	got := typeInSearch(t, m, "xyz/abc")

	m2, _ := got.handleKey(fakeKey("enter"))
	final := m2.(Model)
	if final.status != "E: pattern not found" {
		t.Errorf("status = %q, want 'E: pattern not found'", final.status)
	}
	if final.buf.Content() != "hello\n" {
		t.Errorf("buffer should be unchanged, got %q", final.buf.Content())
	}
}

func TestSearchReplaceBackspacePastSlashLeavesReplaceMode(t *testing.T) {
	m := newTestModel("foo bar\n")
	got := typeInSearch(t, m, "foo/qux")
	if !got.searchReplacing {
		t.Fatal("expected searchReplacing = true")
	}
	// Backspace 4 times: "x","u","q","/" — back to just "foo".
	for range 4 {
		m2, _ := got.handleKey(fakeKey("backspace"))
		got = m2.(Model)
	}
	if got.searchReplacing {
		t.Error("expected searchReplacing = false after backspacing past the delimiter")
	}
	if got.searchQuery != "foo" {
		t.Errorf("searchQuery = %q, want foo", got.searchQuery)
	}
}

func TestBuildSearchOverlaysInjectsGreenPreviewWhenReplacing(t *testing.T) {
	m := newTestModel("foo bar\n")
	got := typeInSearch(t, m, "foo/qux")

	cw := 80
	layout := got.buildScreenLayout(1, cw)
	rows := got.buildSearchOverlays(layout, cw)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	// Expect two overlays: the restyled "foo" match and the injected "qux" preview.
	if len(rows[0]) != 2 {
		t.Fatalf("expected 2 overlays (match + preview), got %d: %+v", len(rows[0]), rows[0])
	}
	preview := rows[0][1]
	if preview.w != 0 {
		t.Errorf("preview overlay w = %d, want 0 (pure injection)", preview.w)
	}
	if preview.col != 3 {
		t.Errorf("preview overlay col = %d, want 3 (right after \"foo\")", preview.col)
	}
}

func TestBuildSearchOverlaysNoPreviewWhenNotReplacing(t *testing.T) {
	m := newTestModel("foo bar\n")
	got := typeInSearch(t, m, "foo")

	cw := 80
	layout := got.buildScreenLayout(1, cw)
	rows := got.buildSearchOverlays(layout, cw)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("expected 1 row with 1 overlay (just the match), got %+v", rows)
	}
}
