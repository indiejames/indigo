package app

import (
	"testing"
)

// newTestPicker builds a filePicker with a fixed file list, bypassing disk I/O.
func newTestPicker(files []string, fuzzySearch bool) *filePicker {
	all := make([]string, len(files))
	copy(all, files)
	fp := &filePicker{
		workDir:     "/fake",
		all:         all,
		filtered:    all,
		fuzzySearch: fuzzySearch,
		width:       80,
		height:      24,
	}
	return fp
}

// TestSetQueryNoSliceAliasing is a regression test for the bug where
// fp.filtered[:0] shared the backing array with fp.all, causing subsequent
// appends to overwrite entries in fp.all, leading to duplicate results.
func TestSetQueryNoSliceAliasing(t *testing.T) {
	files := []string{
		"cmd/indigo/main.go",
		"internal/app/app.go",
		"internal/app/filepicker.go",
		"internal/client/model.go",
		"internal/client/keys.go",
	}
	fp := newTestPicker(files, false)

	// Capture original fp.all slice header to detect aliasing.
	originalAll := make([]string, len(fp.all))
	copy(originalAll, fp.all)

	// Type a query that matches a subset.
	fp.setQuery("main")
	if len(fp.filtered) == 0 {
		t.Fatal("setQuery('main'): expected at least one match")
	}

	// Clear the query — should restore filtered to all.
	fp.setQuery("")

	// fp.all must be intact.
	if len(fp.all) != len(originalAll) {
		t.Fatalf("fp.all length changed: got %d, want %d", len(fp.all), len(originalAll))
	}
	for i, f := range fp.all {
		if f != originalAll[i] {
			t.Errorf("fp.all[%d] = %q, want %q (all was mutated)", i, f, originalAll[i])
		}
	}

	// fp.filtered must equal fp.all (no duplicates, correct length).
	if len(fp.filtered) != len(fp.all) {
		t.Errorf("after clearing query: len(filtered)=%d, want %d", len(fp.filtered), len(fp.all))
	}
}

// TestSetQueryNoDuplicates verifies that calling setQuery multiple times with
// different patterns never produces duplicates in fp.filtered or fp.all.
func TestSetQueryNoDuplicates(t *testing.T) {
	files := []string{
		"cmd/indigo/main.go",
		"internal/app/app.go",
		"internal/app/filepicker.go",
		"internal/client/model.go",
	}
	originalAll := make([]string, len(files))
	copy(originalAll, files)

	for _, fuzzy := range []bool{false, true} {
		fp := newTestPicker(files, fuzzy)

		queries := []string{"main", "app", "", "go", ""}
		for _, q := range queries {
			fp.setQuery(q)
		}

		// fp.all must still be the original slice contents.
		if len(fp.all) != len(originalAll) {
			t.Errorf("fuzzy=%v: fp.all length changed to %d", fuzzy, len(fp.all))
			continue
		}
		for i, f := range fp.all {
			if f != originalAll[i] {
				t.Errorf("fuzzy=%v: fp.all[%d]=%q, want %q", fuzzy, i, f, originalAll[i])
			}
		}

		// After empty query, filtered must equal all with no duplicates.
		seen := map[string]int{}
		for _, f := range fp.filtered {
			seen[f]++
			if seen[f] > 1 {
				t.Errorf("fuzzy=%v: duplicate in filtered: %q", fuzzy, f)
			}
		}
	}
}

// TestSetQuerySubstring checks that substring mode filters correctly.
func TestSetQuerySubstring(t *testing.T) {
	files := []string{
		"cmd/indigo/main.go",
		"internal/app/app.go",
		"internal/client/model.go",
	}
	fp := newTestPicker(files, false)
	fp.setQuery("app")
	if len(fp.filtered) != 1 || fp.filtered[0] != "internal/app/app.go" {
		t.Errorf("substring 'app': got %v, want [internal/app/app.go]", fp.filtered)
	}
}

// TestSetQueryFuzzy checks that fuzzy mode returns matches.
func TestSetQueryFuzzy(t *testing.T) {
	files := []string{
		"cmd/indigo/main.go",
		"internal/app/app.go",
		"internal/client/model.go",
	}
	fp := newTestPicker(files, true)
	fp.setQuery("main")
	found := false
	for _, f := range fp.filtered {
		if f == "cmd/indigo/main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("fuzzy 'main': expected cmd/indigo/main.go in results, got %v", fp.filtered)
	}
}

// TestSetQueryEmptyRestoresBrowse checks that clearing the query returns to
// browse mode and restores fp.filtered to fp.all.
func TestSetQueryEmptyRestoresBrowse(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go"}
	fp := newTestPicker(files, false)
	fp.setQuery("a")
	if fp.browseMode() {
		t.Fatal("expected search mode after non-empty query")
	}
	fp.setQuery("")
	if !fp.browseMode() {
		t.Error("expected browse mode after clearing query")
	}
	if len(fp.filtered) != len(files) {
		t.Errorf("filtered after clear: got %d, want %d", len(fp.filtered), len(files))
	}
}

// TestMoveUpDown checks cursor movement stays in bounds.
// The picker must be in search mode (non-empty query) so moveDown uses fp.filtered.
func TestMoveUpDown(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go"}
	fp := newTestPicker(files, false)
	fp.query = "go" // search mode: moveDown/Up use fp.filtered (len 3)

	fp.moveUp() // should be no-op at top
	if fp.cursor != 0 {
		t.Errorf("moveUp at top: cursor = %d, want 0", fp.cursor)
	}

	fp.moveDown()
	fp.moveDown()
	fp.moveDown() // at last item (index 2), one more should be clamped
	if fp.cursor != 2 {
		t.Errorf("moveDown past end: cursor = %d, want 2", fp.cursor)
	}
}

// TestSelectedPath verifies that selectedPath() returns the correct absolute path
// in search mode (non-empty query).
func TestSelectedPath(t *testing.T) {
	fp := &filePicker{
		workDir:  "/workspace",
		all:      []string{"cmd/main.go"},
		filtered: []string{"cmd/main.go"},
		query:    "main", // search mode
	}
	got := fp.selectedPath()
	want := "/workspace/cmd/main.go"
	if got != want {
		t.Errorf("selectedPath() = %q, want %q", got, want)
	}
}

// TestSelectedEmpty verifies that selectedPath() returns "" when no file is highlighted.
func TestSelectedEmpty(t *testing.T) {
	// Browse mode with no entries returns "".
	fp := &filePicker{workDir: "/workspace"}
	if got := fp.selectedPath(); got != "" {
		t.Errorf("selectedPath() on empty browse picker = %q, want empty string", got)
	}
	// Search mode with empty filtered also returns "".
	fp2 := &filePicker{workDir: "/workspace", query: "x"}
	if got := fp2.selectedPath(); got != "" {
		t.Errorf("selectedPath() on empty search picker = %q, want empty string", got)
	}
}
