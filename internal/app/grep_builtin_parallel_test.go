package app

import (
	"fmt"
	"testing"
)

// TestSearchBuiltinParallelFindsAllMatchesAcrossManyFiles is a correctness
// check for parallelizing searchBuiltin's per-file scanning across a worker
// pool: every file's matches must still show up, each with correct
// path/line/col, regardless of which goroutine happened to process it.
func TestSearchBuiltinParallelFindsAllMatchesAcrossManyFiles(t *testing.T) {
	const numFiles = 60
	files := make(map[string]string, numFiles)
	for i := 0; i < numFiles; i++ {
		if i%2 == 0 {
			files[fmt.Sprintf("pkg%d/file.go", i)] = "package pkg\nfunc TargetMatch() {}\n"
		} else {
			files[fmt.Sprintf("pkg%d/file.go", i)] = "package pkg\nfunc NoMatchHere() {}\n"
		}
	}
	dir := writeTree(t, files)

	results, err := searchBuiltin(dir, "TargetMatch", "", "", true, false)
	if err != nil {
		t.Fatalf("searchBuiltin: %v", err)
	}
	if len(results) != numFiles/2 {
		t.Fatalf("got %d results, want %d (one per matching file)", len(results), numFiles/2)
	}
	seen := map[string]bool{}
	for _, r := range results {
		if r.Line != 1 {
			t.Errorf("result %+v: line = %d, want 1", r, r.Line)
		}
		seen[r.RelPath] = true
	}
	if len(seen) != numFiles/2 {
		t.Errorf("expected matches from %d distinct files, got %d", numFiles/2, len(seen))
	}
}

// TestSearchBuiltinParallelRespectsMaxResults is a regression test:
// parallelizing the per-file scan must not let the total result count grow
// past maxGrepResults even though multiple workers append concurrently —
// the final concatenation step must still hard-cap the slice.
func TestSearchBuiltinParallelRespectsMaxResults(t *testing.T) {
	const numFiles = 40
	files := make(map[string]string, numFiles)
	var content string
	for i := 0; i < 30; i++ {
		content += "match\n"
	}
	for i := 0; i < numFiles; i++ {
		files[fmt.Sprintf("file%d.txt", i)] = content
	}
	dir := writeTree(t, files)

	results, err := searchBuiltin(dir, "match", "", "", true, false)
	if err != nil {
		t.Fatalf("searchBuiltin: %v", err)
	}
	if len(results) > maxGrepResults {
		t.Fatalf("got %d results, want <= %d (maxGrepResults)", len(results), maxGrepResults)
	}
	if len(results) == 0 {
		t.Fatal("expected some results")
	}
}
