package lint

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/indiejames/indigo/internal/lsp"
)

// parsers maps a config.LinterConfig.Format value to the function that turns
// that tool's raw output into diagnostics.
var parsers = map[string]func(out []byte, filePath string) ([]lsp.Diagnostic, error){
	"golangci-lint-json": parseGolangciLint,
}

// golangciOutput models the subset of `golangci-lint run --out-format json`
// this package cares about.
type golangciOutput struct {
	Issues []struct {
		FromLinter string `json:"FromLinter"`
		Text       string `json:"Text"`
		Severity   string `json:"Severity"`
		Pos        struct {
			Line   int `json:"Line"`
			Column int `json:"Column"`
		} `json:"Pos"`
	} `json:"Issues"`
}

// parseGolangciLint converts golangci-lint's JSON report into diagnostics.
// Pos.Line/Column are 1-based; lsp.Position is 0-based. Severity is commonly
// left empty (golangci-lint doesn't set it by default), so an empty/unknown
// value is treated as a warning rather than dropped.
func parseGolangciLint(out []byte, _ string) ([]lsp.Diagnostic, error) {
	var parsed golangciOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("golangci-lint: parse output: %w", err)
	}

	diags := make([]lsp.Diagnostic, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		line := max(0, iss.Pos.Line-1)
		col := max(0, iss.Pos.Column-1)
		source := "golangci-lint"
		if iss.FromLinter != "" {
			source = "golangci-lint:" + iss.FromLinter
		}
		diags = append(diags, lsp.Diagnostic{
			Range: lsp.Range{
				Start: lsp.Position{Line: line, Character: col},
				End:   lsp.Position{Line: line, Character: col + 1},
			},
			Severity: golangciSeverity(iss.Severity),
			Source:   source,
			Message:  iss.Text,
		})
	}
	return diags, nil
}

func golangciSeverity(s string) lsp.DiagnosticSeverity {
	switch strings.ToLower(s) {
	case "error":
		return lsp.SeverityError
	case "info", "information":
		return lsp.SeverityInformation
	case "hint":
		return lsp.SeverityHint
	default:
		return lsp.SeverityWarning
	}
}
