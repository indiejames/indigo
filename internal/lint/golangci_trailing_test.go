package lint

import "testing"

// TestParseGolangciEntriesIgnoresTrailingSummary is a regression test for a
// bug that silently disabled Go linting entirely.
//
// golangci-lint v2, invoked as the default config does
// (`run --output.json.path=stdout ./...`), writes its JSON report and *then* a
// human-readable summary to the same stream. The parser used json.Unmarshal,
// which is strict about trailing data, so it rejected the whole payload as
// malformed. Every run — per-file and whole-workspace — was discarded as a
// parse error, and Go diagnostics silently never appeared: no error surfaced
// anywhere the user would see, the results were simply absent.
func TestParseGolangciEntriesIgnoresTrailingSummary(t *testing.T) {
	// Shape taken from real golangci-lint 2.11.4 output.
	const out = `{"Issues":[{"FromLinter":"staticcheck","Text":"SA4011: ineffective break statement","Pos":{"Filename":"internal/x/a.go","Line":10,"Column":5}}],"Report":{}}
14 issues:
* staticcheck: 1
* unused: 13
`

	entries, err := parseGolangciEntries([]byte(out))
	if err != nil {
		t.Fatalf("parseGolangciEntries: %v — the trailing summary after the JSON must be "+
			"ignored, not treated as malformed output", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].file != "internal/x/a.go" {
		t.Errorf("file = %q, want %q", entries[0].file, "internal/x/a.go")
	}
	if entries[0].diag.Range.Start.Line != 9 { // 1-based in, 0-based out
		t.Errorf("line = %d, want 9", entries[0].diag.Range.Start.Line)
	}
}

// TestParseGolangciEntriesStillRejectsGarbage pins that the fix loosened only
// the trailing-data rule: output that isn't JSON at all must still be an
// error, so a broken linter invocation is reported rather than read as "no
// issues found".
func TestParseGolangciEntriesStillRejectsGarbage(t *testing.T) {
	if _, err := parseGolangciEntries([]byte("command not found\n")); err == nil {
		t.Error("non-JSON output parsed without error; a failed linter run would be " +
			"indistinguishable from a clean one")
	}
}
