package lint

import (
	"encoding/json"
	"fmt"

	"github.com/indiejames/indigo/internal/lsp"
)

func init() {
	parsers["eslint-json"] = parseESLint
}

// eslintResult models one entry of `eslint --format json`'s output: an
// array of per-file results. Only Messages is used — the rest (errorCount,
// fixable counts, ...) isn't needed for diagnostics.
type eslintResult struct {
	Messages []struct {
		RuleID    string `json:"ruleId"`
		Severity  int    `json:"severity"` // 1 = warning, 2 = error
		Message   string `json:"message"`
		Line      int    `json:"line"`
		Column    int    `json:"column"`
		EndLine   int    `json:"endLine"`
		EndColumn int    `json:"endColumn"`
	} `json:"messages"`
}

// parseESLint converts eslint's JSON report into diagnostics. Line/Column
// (and EndLine/EndColumn, when present) are 1-based; lsp.Position is
// 0-based. A handful of message kinds (e.g. a parse error) carry no end
// position — those fall back to a single-character range at the start.
func parseESLint(out []byte, _ string) ([]lsp.Diagnostic, error) {
	var results []eslintResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("eslint: parse output: %w", err)
	}

	var diags []lsp.Diagnostic
	for _, r := range results {
		for _, m := range r.Messages {
			startLine := max(0, m.Line-1)
			startCol := max(0, m.Column-1)
			endLine, endCol := startLine, startCol+1
			if m.EndLine > 0 {
				endLine = max(0, m.EndLine-1)
				endCol = max(0, m.EndColumn-1)
			}
			source := "eslint"
			if m.RuleID != "" {
				source = "eslint:" + m.RuleID
			}
			diags = append(diags, lsp.Diagnostic{
				Range: lsp.Range{
					Start: lsp.Position{Line: startLine, Character: startCol},
					End:   lsp.Position{Line: endLine, Character: endCol},
				},
				Severity: eslintSeverity(m.Severity),
				Source:   source,
				Message:  m.Message,
			})
		}
	}
	return diags, nil
}

func eslintSeverity(sev int) lsp.DiagnosticSeverity {
	if sev >= 2 {
		return lsp.SeverityError
	}
	return lsp.SeverityWarning
}
