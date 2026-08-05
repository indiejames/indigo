package lint

import (
	"testing"

	"github.com/indiejames/indigo/internal/lsp"
)

func TestParseRuffBasic(t *testing.T) {
	out := []byte(`[
		{
			"code": "F401",
			"message": "` + "`os`" + ` imported but unused",
			"filename": "/abs/foo.py",
			"location": {"row": 1, "column": 8},
			"end_location": {"row": 1, "column": 10}
		}
	]`)

	diags, err := parseRuff(out, "/abs/foo.py")
	if err != nil {
		t.Fatalf("parseRuff: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1", len(diags))
	}

	d := diags[0]
	if d.Range.Start != (lsp.Position{Line: 0, Character: 7}) {
		t.Errorf("start = %+v, want {0 7}", d.Range.Start)
	}
	if d.Range.End != (lsp.Position{Line: 0, Character: 9}) {
		t.Errorf("end = %+v, want {0 9}", d.Range.End)
	}
	if d.Severity != lsp.SeverityWarning {
		t.Errorf("severity = %v, want SeverityWarning (ruff has no severity field)", d.Severity)
	}
	if d.Source != "ruff:F401" {
		t.Errorf("source = %q, want ruff:F401", d.Source)
	}
}

func TestParseRuffEmpty(t *testing.T) {
	diags, err := parseRuff([]byte(`[]`), "foo.py")
	if err != nil {
		t.Fatalf("parseRuff: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0", len(diags))
	}
}

func TestParseRuffInvalidJSON(t *testing.T) {
	if _, err := parseRuff([]byte("not json"), "foo.py"); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}
