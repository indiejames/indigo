package lint

import (
	"encoding/json"
	"fmt"

	"github.com/indiejames/indigo/internal/lsp"
)

func init() {
	parsers["ruff-json"] = parseRuff
}

// ruffDiagnostic models one entry of `ruff check --output-format json`'s
// output: a flat array, unlike eslint's per-file grouping.
type ruffDiagnostic struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Location struct {
		Row    int `json:"row"`
		Column int `json:"column"`
	} `json:"location"`
	EndLocation struct {
		Row    int `json:"row"`
		Column int `json:"column"`
	} `json:"end_location"`
}

// parseRuff converts ruff's JSON report into diagnostics. Row/Column are
// 1-based; lsp.Position is 0-based. Ruff's JSON schema has no severity
// field — every entry is a rule violation someone would want to see, so all
// are reported as warnings rather than guessed at.
func parseRuff(out []byte, _ string) ([]lsp.Diagnostic, error) {
	var results []ruffDiagnostic
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("ruff: parse output: %w", err)
	}

	diags := make([]lsp.Diagnostic, 0, len(results))
	for _, d := range results {
		startLine := max(0, d.Location.Row-1)
		startCol := max(0, d.Location.Column-1)
		endLine := max(startLine, d.EndLocation.Row-1)
		endCol := max(startCol+1, d.EndLocation.Column-1)
		source := "ruff"
		if d.Code != "" {
			source = "ruff:" + d.Code
		}
		diags = append(diags, lsp.Diagnostic{
			Range: lsp.Range{
				Start: lsp.Position{Line: startLine, Character: startCol},
				End:   lsp.Position{Line: endLine, Character: endCol},
			},
			Severity: lsp.SeverityWarning,
			Source:   source,
			Message:  d.Message,
		})
	}
	return diags, nil
}
