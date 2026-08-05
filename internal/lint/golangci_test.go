package lint

import (
	"testing"

	"github.com/indiejames/indigo/internal/lsp"
)

func TestParseGolangciLintBasic(t *testing.T) {
	out := []byte(`{
		"Issues": [
			{
				"FromLinter": "unused",
				"Text": "func foo is unused",
				"Severity": "",
				"Pos": {"Filename": "foo.go", "Line": 12, "Column": 6}
			},
			{
				"FromLinter": "errcheck",
				"Text": "Error return value is not checked",
				"Severity": "error",
				"Pos": {"Filename": "foo.go", "Line": 5, "Column": 2}
			}
		]
	}`)

	diags, err := parseGolangciLint(out, "foo.go")
	if err != nil {
		t.Fatalf("parseGolangciLint: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("len(diags) = %d, want 2", len(diags))
	}

	d0 := diags[0]
	if d0.Range.Start.Line != 11 || d0.Range.Start.Character != 5 {
		t.Errorf("diag0 start = %+v, want {11 5} (0-based)", d0.Range.Start)
	}
	if d0.Severity != lsp.SeverityWarning {
		t.Errorf("diag0 severity = %v, want SeverityWarning (empty defaults to warning)", d0.Severity)
	}
	if d0.Source != "golangci-lint:unused" {
		t.Errorf("diag0 source = %q, want %q", d0.Source, "golangci-lint:unused")
	}
	if d0.Message != "func foo is unused" {
		t.Errorf("diag0 message = %q", d0.Message)
	}

	d1 := diags[1]
	if d1.Range.Start.Line != 4 || d1.Range.Start.Character != 1 {
		t.Errorf("diag1 start = %+v, want {4 1} (0-based)", d1.Range.Start)
	}
	if d1.Severity != lsp.SeverityError {
		t.Errorf("diag1 severity = %v, want SeverityError", d1.Severity)
	}
}

func TestParseGolangciLintNoIssues(t *testing.T) {
	diags, err := parseGolangciLint([]byte(`{"Issues": []}`), "foo.go")
	if err != nil {
		t.Fatalf("parseGolangciLint: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0", len(diags))
	}
}

func TestParseGolangciLintInvalidJSON(t *testing.T) {
	_, err := parseGolangciLint([]byte("not json"), "foo.go")
	if err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestGolangciSeverityMapping(t *testing.T) {
	cases := map[string]lsp.DiagnosticSeverity{
		"error":       lsp.SeverityError,
		"ERROR":       lsp.SeverityError,
		"info":        lsp.SeverityInformation,
		"information": lsp.SeverityInformation,
		"hint":        lsp.SeverityHint,
		"":            lsp.SeverityWarning,
		"warning":     lsp.SeverityWarning,
		"bogus":       lsp.SeverityWarning,
	}
	for in, want := range cases {
		if got := golangciSeverity(in); got != want {
			t.Errorf("golangciSeverity(%q) = %v, want %v", in, got, want)
		}
	}
}
