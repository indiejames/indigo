package lint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/indiejames/indigo/internal/lsp"
)

func init() {
	parsers["cargo-clippy-json"] = parseCargoClippy
	workspaceParsers["cargo-clippy-json"] = parseCargoClippyWorkspace
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
			FileName    string `json:"file_name"`
			LineStart   int    `json:"line_start"`
			ColumnStart int    `json:"column_start"`
			LineEnd     int    `json:"line_end"`
			ColumnEnd   int    `json:"column_end"`
			IsPrimary   bool   `json:"is_primary"`
		} `json:"spans"`
	} `json:"message"`
}

// parseCargoClippy converts cargo/clippy's report (see
// parseCargoClippyEntries) into diagnostics for a single file, filtered to
// only those whose primary span's file_name matches the provided filePath
// (normalized for consistent comparison).
func parseCargoClippy(out []byte, filePath string) ([]lsp.Diagnostic, error) {
	var diags []lsp.Diagnostic
	normalizedFilePath := filepath.Clean(filePath)
	for _, entry := range parseCargoClippyEntries(out) {
		if filepath.Clean(entry.file) != normalizedFilePath {
			continue
		}
		diags = append(diags, entry.diag)
	}
	return diags, nil
}

// parseCargoClippyWorkspace converts a whole-crate `cargo clippy
// --message-format json` report into a path -> diagnostics map, grouping by
// each message's own primary span file instead of filtering down to one
// caller-supplied path.
func parseCargoClippyWorkspace(out []byte, workDir string) (map[string][]lsp.Diagnostic, error) {
	byPath := make(map[string][]lsp.Diagnostic)
	for _, entry := range parseCargoClippyEntries(out) {
		path := resolveScanPath(workDir, entry.file)
		byPath[path] = append(byPath[path], entry.diag)
	}
	return byPath, nil
}

type cargoClippyEntry struct {
	file string
	diag lsp.Diagnostic
}

// parseCargoClippyEntries parses cargo/clippy's newline-delimited JSON
// output one line at a time — unlike the other tools' output, this isn't a
// single JSON document, so a malformed or unrelated line (cargo sometimes
// interleaves plain-text progress output) is skipped rather than failing
// the whole parse. Only a message's primary span is used: a rustc
// diagnostic often carries secondary spans too (e.g. "note: previous
// definition here" pointing elsewhere in the file), which would otherwise
// show as extra, misleadingly-placed markers for what is really one issue.
// line_start/column_start are 1-based; lsp.Position is 0-based.
func parseCargoClippyEntries(out []byte) []cargoClippyEntry {
	var entries []cargoClippyEntry
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
			entries = append(entries, cargoClippyEntry{
				file: s.FileName,
				diag: lsp.Diagnostic{
					Range: lsp.Range{
						Start: lsp.Position{Line: startLine, Character: startCol},
						End:   lsp.Position{Line: endLine, Character: endCol},
					},
					Severity: clippySeverity(msg.Message.Level),
					Source:   "clippy",
					Message:  msg.Message.Message,
				},
			})
			break
		}
	}
	return entries
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
