package lint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	"github.com/indiejames/indigo/internal/lsp"
)

func init() {
	parsers["cargo-clippy-json"] = parseCargoClippy
}

// cargoMessage is one line of `cargo clippy --message-format json`'s
// newline-delimited output. Only the "compiler-message" reason carries a
// rustc-shaped diagnostic; other reasons ("compiler-artifact",
// "build-finished", ...) are skipped.
type cargoMessage struct {
	Reason  string `json:"reason"`
	Message *struct {
		Message string `json:"message"`
		Level   string `json:"level"`
		Spans   []struct {
			LineStart   int  `json:"line_start"`
			ColumnStart int  `json:"column_start"`
			LineEnd     int  `json:"line_end"`
			ColumnEnd   int  `json:"column_end"`
			IsPrimary   bool `json:"is_primary"`
		} `json:"spans"`
	} `json:"message"`
}

// parseCargoClippy converts cargo/clippy's newline-delimited JSON output
// into diagnostics, one line at a time — unlike the other parsers, this
// isn't a single JSON document, so a malformed or unrelated line (cargo
// sometimes interleaves plain-text progress output) is skipped rather than
// failing the whole parse. Only a message's primary span is used: a rustc
// diagnostic often carries secondary spans too (e.g. "note: previous
// definition here" pointing elsewhere in the file), which would otherwise
// show as extra, misleadingly-placed markers for what is really one issue.
// line_start/column_start are 1-based; lsp.Position is 0-based.
func parseCargoClippy(out []byte, _ string) ([]lsp.Diagnostic, error) {
	var diags []lsp.Diagnostic
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg cargoMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Reason != "compiler-message" || msg.Message == nil {
			continue
		}
		for _, s := range msg.Message.Spans {
			if !s.IsPrimary {
				continue
			}
			startLine := max(0, s.LineStart-1)
			startCol := max(0, s.ColumnStart-1)
			endLine := max(startLine, s.LineEnd-1)
			endCol := max(startCol+1, s.ColumnEnd-1)
			diags = append(diags, lsp.Diagnostic{
				Range: lsp.Range{
					Start: lsp.Position{Line: startLine, Character: startCol},
					End:   lsp.Position{Line: endLine, Character: endCol},
				},
				Severity: clippySeverity(msg.Message.Level),
				Source:   "clippy",
				Message:  msg.Message.Message,
			})
			break
		}
	}
	return diags, nil
}

func clippySeverity(level string) lsp.DiagnosticSeverity {
	switch level {
	case "error":
		return lsp.SeverityError
	case "note":
		return lsp.SeverityInformation
	case "help":
		return lsp.SeverityHint
	default: // "warning" and anything else
		return lsp.SeverityWarning
	}
}
