package lint

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/lsp"
)

func TestParseCargoClippyBasic(t *testing.T) {
	// Two NDJSON lines: one compiler-message with a primary + secondary
	// span, and one unrelated reason that must be skipped.
	out := []byte(`{"reason":"compiler-message","message":{"message":"unused variable: ` + "`x`" + `","level":"warning","spans":[{"file_name":"src/main.rs","line_start":3,"column_start":9,"line_end":3,"column_end":10,"is_primary":true},{"file_name":"src/main.rs","line_start":10,"column_start":1,"line_end":10,"column_end":2,"is_primary":false}]}}
{"reason":"build-finished","success":true}
{"reason":"compiler-message","message":{"message":"this could be written as ` + "`x + 1`" + `","level":"error","spans":[{"file_name":"src/main.rs","line_start":7,"column_start":5,"line_end":7,"column_end":12,"is_primary":true}]}}
`)

	diags, err := parseCargoClippy(out, "src/main.rs")
	if err != nil {
		t.Fatalf("parseCargoClippy: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("len(diags) = %d, want 2 (build-finished line skipped)", len(diags))
	}

	d0 := diags[0]
	if d0.Range.Start != (lsp.Position{Line: 2, Character: 8}) {
		t.Errorf("diag0 start = %+v, want {2 8} (primary span, not the secondary at line 10)", d0.Range.Start)
	}
	if d0.Severity != lsp.SeverityWarning {
		t.Errorf("diag0 severity = %v, want SeverityWarning", d0.Severity)
	}
	if d0.Source != "clippy" {
		t.Errorf("diag0 source = %q, want clippy", d0.Source)
	}

	d1 := diags[1]
	if d1.Severity != lsp.SeverityError {
		t.Errorf("diag1 severity = %v, want SeverityError", d1.Severity)
	}
}

func TestParseCargoClippyNoPrimarySpan(t *testing.T) {
	// A message whose spans are all secondary contributes no diagnostic
	// rather than guessing at a location.
	out := []byte(`{"reason":"compiler-message","message":{"message":"note only","level":"note","spans":[{"file_name":"src/main.rs","line_start":1,"column_start":1,"line_end":1,"column_end":2,"is_primary":false}]}}
`)
	diags, err := parseCargoClippy(out, "src/main.rs")
	if err != nil {
		t.Fatalf("parseCargoClippy: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0", len(diags))
	}
}

func TestParseCargoClippySkipsUnrelatedLines(t *testing.T) {
	out := []byte("not json at all\n{\"reason\":\"compiler-artifact\"}\n")
	diags, err := parseCargoClippy(out, "src/main.rs")
	if err != nil {
		t.Fatalf("parseCargoClippy should not error on unrelated/malformed lines: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0", len(diags))
	}
}

func TestParseCargoClippyFiltersByFile(t *testing.T) {
	// Three diagnostics: one for src/main.rs, one for src/lib.rs, one for src/helper.rs.
	// When parsing for src/main.rs, only the first should be included.
	out := []byte(`{"reason":"compiler-message","message":{"message":"unused variable in main","level":"warning","spans":[{"file_name":"src/main.rs","line_start":5,"column_start":9,"line_end":5,"column_end":10,"is_primary":true}]}}
{"reason":"compiler-message","message":{"message":"unused function in lib","level":"warning","spans":[{"file_name":"src/lib.rs","line_start":10,"column_start":4,"line_end":10,"column_end":8,"is_primary":true}]}}
{"reason":"compiler-message","message":{"message":"unused import in helper","level":"warning","spans":[{"file_name":"src/helper.rs","line_start":1,"column_start":5,"line_end":1,"column_end":15,"is_primary":true}]}}
`)
	diags, err := parseCargoClippy(out, "src/main.rs")
	if err != nil {
		t.Fatalf("parseCargoClippy: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1 (only src/main.rs diagnostic should be included)", len(diags))
	}
	if !strings.Contains(diags[0].Message, "unused variable in main") {
		t.Errorf("expected diagnostic about main.rs, got %q", diags[0].Message)
	}
}
