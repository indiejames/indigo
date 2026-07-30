package client

import "testing"

// TestFilterCompletionsSurfacesAutoImport reproduces the real tsserver response
// shape: an auto-import candidate (greetLoudly) carries sortText "￿16" so it
// sorts dead last in the raw list, buried behind in-scope globals. Filtering by
// the typed prefix "greetL" must lift it to the top so it's visible/acceptable.
func TestFilterCompletionsSurfacesAutoImport(t *testing.T) {
	raw := []ClientCompletion{
		{Label: "globalThis", SortText: "15"},
		{Label: "GreenScreen", SortText: "15"},
		{Label: "yield", SortText: "15"},
		{Label: "greetLoudly", SortText: "￿16", Detail: "./helper"},
		{Label: "AudioProcessingEvent", SortText: "z15"},
	}

	got := filterCompletions(raw, "greetL")

	if len(got) == 0 {
		t.Fatal("filtering 'greetL' returned no items; greetLoudly should match")
	}
	if got[0].Label != "greetLoudly" {
		labels := make([]string, len(got))
		for i, c := range got {
			labels[i] = c.Label
		}
		t.Errorf("top match = %q, want greetLoudly (ranked order: %v)", got[0].Label, labels)
	}
}

// TestFilterCompletionsPrefixBeatsSubsequence checks the ranking tiers: a prefix
// match outranks a mere subsequence match even when the subsequence item has a
// lower sortText.
func TestFilterCompletionsPrefixBeatsSubsequence(t *testing.T) {
	raw := []ClientCompletion{
		{Label: "gRoupExpandLisp", SortText: "01"}, // subsequence of "grel", low sortText
		{Label: "grelInline", SortText: "99"},      // prefix match, high sortText
	}
	got := filterCompletions(raw, "grel")
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	if got[0].Label != "grelInline" {
		t.Errorf("top match = %q, want grelInline (prefix match should beat subsequence)", got[0].Label)
	}
}

// TestFilterCompletionsEmptyPrefixKeepsSortText verifies that with no prefix
// (e.g. member completion right after '.') items are kept and ordered by the
// server's sortText.
func TestFilterCompletionsEmptyPrefixKeepsSortText(t *testing.T) {
	raw := []ClientCompletion{
		{Label: "zeta", SortText: "11"},
		{Label: "alpha", SortText: "12"},
	}
	got := filterCompletions(raw, "")
	if len(got) != 2 {
		t.Fatalf("empty prefix should keep all items, got %d", len(got))
	}
	if got[0].Label != "zeta" {
		t.Errorf("top = %q, want zeta (lower sortText wins)", got[0].Label)
	}
}

// TestFilterCompletionsDropsNonMatches ensures items that don't match at all are
// removed.
func TestFilterCompletionsDropsNonMatches(t *testing.T) {
	raw := []ClientCompletion{
		{Label: "greetLoudly"},
		{Label: "xyz"},
	}
	got := filterCompletions(raw, "greet")
	if len(got) != 1 || got[0].Label != "greetLoudly" {
		t.Errorf("want only greetLoudly, got %+v", got)
	}
}

// TestCompletionContinues checks which keys keep an open completion popup alive
// (word chars and backspace) versus dismissing it.
func TestCompletionContinues(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"a", true},
		{"Z", true},
		{"9", true},
		{"_", true},
		{"backspace", true},
		{" ", false},
		{".", false},
		{"(", false},
		{"esc", false},
		{"enter", false},
	}
	for _, tc := range cases {
		if got := completionContinues(fakeKey(tc.key)); got != tc.want {
			t.Errorf("completionContinues(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

// TestRefreshCompletionFilterNarrows verifies that as the typed prefix grows,
// the cached list is re-filtered (narrowed) without a re-fetch.
func TestRefreshCompletionFilterNarrows(t *testing.T) {
	m := newTestModel("greetLo\n")
	m.cursor.Line, m.cursor.Col = 0, 7 // just after "greetLo"
	m.completionOn = true
	m.completionsRaw = []ClientCompletion{
		{Label: "greetLoudly", SortText: "￿16"},
		{Label: "greenField", SortText: "11"},
		{Label: "globalThis", SortText: "15"},
	}
	m.completions = m.completionsRaw

	got := m.refreshCompletionFilter()

	if !got.completionOn {
		t.Fatal("popup dismissed, but greetLoudly still matches 'greetLo'")
	}
	if len(got.completions) != 1 || got.completions[0].Label != "greetLoudly" {
		t.Errorf("narrowed list = %+v, want just greetLoudly", got.completions)
	}
	if got.completionPrefix != "greetLo" {
		t.Errorf("prefix = %q, want greetLo", got.completionPrefix)
	}
}

// TestRefreshCompletionFilterDismissesOnNoMatch verifies the popup closes when
// the prefix no longer matches anything.
func TestRefreshCompletionFilterDismissesOnNoMatch(t *testing.T) {
	m := newTestModel("zzq\n")
	m.cursor.Line, m.cursor.Col = 0, 3
	m.completionOn = true
	m.completionsRaw = []ClientCompletion{{Label: "greetLoudly"}}
	m.completions = m.completionsRaw

	got := m.refreshCompletionFilter()

	if got.completionOn {
		t.Error("popup should be dismissed when nothing matches 'zzq'")
	}
	if got.completions != nil || got.completionsRaw != nil {
		t.Error("completion state should be cleared on dismiss")
	}
}

// TestHandleInsertNarrowsOpenPopup drives the full insert path: with the popup
// open, typing a word character inserts it AND narrows the list in place rather
// than dismissing it (the reported "typing closes the popup" bug).
func TestHandleInsertNarrowsOpenPopup(t *testing.T) {
	m := newTestModel("greetLo\n")
	m.mode = ModeInsert
	m.rpc = &RPC{}
	m.cursor.Line, m.cursor.Col = 0, 7
	m.completionOn = true
	m.completionPrefix = "greetLo"
	m.completionsRaw = []ClientCompletion{
		{Label: "greetLoudly", SortText: "￿16"},
		{Label: "globalThis", SortText: "15"},
	}
	m.completions = m.completionsRaw

	res, _ := m.handleInsert(fakeKey("u"))
	got := res.(Model)

	if got.buf.Line(0) != "greetLou" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "greetLou")
	}
	if !got.completionOn {
		t.Fatal("popup was dismissed by typing; expected it to stay open and narrow")
	}
	if len(got.completions) != 1 || got.completions[0].Label != "greetLoudly" {
		t.Errorf("narrowed list = %+v, want just greetLoudly", got.completions)
	}
}
