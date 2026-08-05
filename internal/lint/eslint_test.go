package lint

import (
	"testing"

	"github.com/indiejames/indigo/internal/lsp"
)

func TestParseESLintBasic(t *testing.T) {
	out := []byte(`[
		{
			"filePath": "/abs/foo.js",
			"messages": [
				{"ruleId": "no-unused-vars", "severity": 2, "message": "'x' is defined but never used.", "line": 3, "column": 7, "endLine": 3, "endColumn": 8},
				{"ruleId": "no-console", "severity": 1, "message": "Unexpected console statement.", "line": 10, "column": 1}
			],
			"errorCount": 1,
			"warningCount": 1
		}
	]`)

	diags, err := parseESLint(out, "/abs/foo.js")
	if err != nil {
		t.Fatalf("parseESLint: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("len(diags) = %d, want 2", len(diags))
	}

	d0 := diags[0]
	if d0.Range.Start != (lsp.Position{Line: 2, Character: 6}) {
		t.Errorf("diag0 start = %+v, want {2 6}", d0.Range.Start)
	}
	if d0.Range.End != (lsp.Position{Line: 2, Character: 7}) {
		t.Errorf("diag0 end = %+v, want {2 7}", d0.Range.End)
	}
	if d0.Severity != lsp.SeverityError {
		t.Errorf("diag0 severity = %v, want SeverityError", d0.Severity)
	}
	if d0.Source != "eslint:no-unused-vars" {
		t.Errorf("diag0 source = %q", d0.Source)
	}

	// No endLine/endColumn: falls back to a single-character range.
	d1 := diags[1]
	if d1.Range.Start != (lsp.Position{Line: 9, Character: 0}) {
		t.Errorf("diag1 start = %+v, want {9 0}", d1.Range.Start)
	}
	if d1.Range.End != (lsp.Position{Line: 9, Character: 1}) {
		t.Errorf("diag1 end = %+v, want {9 1} (fallback single-char range)", d1.Range.End)
	}
	if d1.Severity != lsp.SeverityWarning {
		t.Errorf("diag1 severity = %v, want SeverityWarning", d1.Severity)
	}
}

func TestParseESLintNoMessages(t *testing.T) {
	diags, err := parseESLint([]byte(`[{"filePath": "/abs/foo.js", "messages": [], "errorCount": 0, "warningCount": 0}]`), "/abs/foo.js")
	if err != nil {
		t.Fatalf("parseESLint: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0", len(diags))
	}
}

func TestParseESLintInvalidJSON(t *testing.T) {
	if _, err := parseESLint([]byte("not json"), "foo.js"); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}
