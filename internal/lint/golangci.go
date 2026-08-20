package lint

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/indiejames/indigo/internal/lsp"
)

// parsers maps a config.LinterConfig.Format value to the function that turns
// that tool's raw output into diagnostics for a single, already-known file.
var parsers = map[string]func(out []byte, filePath string) ([]lsp.Diagnostic, error){
	"golangci-lint-json": parseGolangciLint,
}

// workspaceParsers is parsers' multi-file counterpart, used by
// Manager.ScanWorkspace: the tool's own output identifies which file each
// diagnostic belongs to (parsers ignores this, since a single-file run only
// ever has one candidate), grouped into a path -> diagnostics map. workDir
// resolves any relative path the tool reports.
var workspaceParsers = map[string]func(out []byte, workDir string) (map[string][]lsp.Diagnostic, error){
	"golangci-lint-json": parseGolangciLintWorkspace,
}

// resolveScanPath joins a possibly-relative path reported by a workspace-
// scan linter run against workDir (the process's cwd during the run) and
// cleans the result, so files.WorkspaceScanSnapshot's keys match the
// absolute paths buffers/paths are keyed by elsewhere in the server.
func resolveScanPath(workDir, raw string) string {
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw)
	}
	return filepath.Clean(filepath.Join(workDir, raw))
}

// golangciOutput models the subset of `golangci-lint run --out-format json`
// this package cares about.
type golangciOutput struct {
	Issues []struct {
		FromLinter string `json:"FromLinter"`
		Text       string `json:"Text"`
		Severity   string `json:"Severity"`
		Pos        struct {
			Filename string `json:"Filename"`
			Line     int    `json:"Line"`
			Column   int    `json:"Column"`
		} `json:"Pos"`
	} `json:"Issues"`
}

// golangciEntry pairs one issue's diagnostic with the file it was reported
// against — parseGolangciLint (single, caller-supplied file) and
// parseGolangciLintWorkspace (multi-file, grouped by this field) share the
// same underlying conversion via parseGolangciEntries and differ only in
// what they do with entry.file.
type golangciEntry struct {
	file string
	diag lsp.Diagnostic
}

// parseGolangciEntries converts golangci-lint's JSON report into
// (file, diagnostic) pairs. Pos.Line/Column are 1-based; lsp.Position is
// 0-based. Severity is commonly left empty (golangci-lint doesn't set it by
// default), so an empty/unknown value is treated as a warning rather than
// dropped.
func parseGolangciEntries(out []byte) ([]golangciEntry, error) {
	var parsed golangciOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("golangci-lint: parse output: %w", err)
	}

	entries := make([]golangciEntry, 0, len(parsed.Issues))
	for _, iss := range parsed.Issues {
		line := max(0, iss.Pos.Line-1)
		col := max(0, iss.Pos.Column-1)
		source := "golangci-lint"
		if iss.FromLinter != "" {
			source = "golangci-lint:" + iss.FromLinter
		}
		entries = append(entries, golangciEntry{
			file: iss.Pos.Filename,
			diag: lsp.Diagnostic{
				Range: lsp.Range{
					Start: lsp.Position{Line: line, Character: col},
					End:   lsp.Position{Line: line, Character: col + 1},
				},
				Severity: golangciSeverity(iss.Severity),
				Source:   source,
				Message:  iss.Text,
			},
		})
	}
	return entries, nil
}

// parseGolangciLint converts golangci-lint's JSON report into diagnostics
// for a single, already-known file — every entry is included regardless of
// its own Pos.Filename, since a single-file invocation (`golangci-lint run
// {file}`) only ever reports issues about that one file to begin with.
func parseGolangciLint(out []byte, _ string) ([]lsp.Diagnostic, error) {
	entries, err := parseGolangciEntries(out)
	if err != nil {
		return nil, err
	}
	diags := make([]lsp.Diagnostic, 0, len(entries))
	for _, e := range entries {
		diags = append(diags, e.diag)
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

// parseGolangciLintWorkspace converts a `golangci-lint run ./...`-style
// whole-project report into a path -> diagnostics map, keyed by each
// issue's own Pos.Filename rather than a single caller-supplied path.
func parseGolangciLintWorkspace(out []byte, workDir string) (map[string][]lsp.Diagnostic, error) {
	entries, err := parseGolangciEntries(out)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string][]lsp.Diagnostic)
	for _, e := range entries {
		if e.file == "" {
			continue
		}
		path := resolveScanPath(workDir, e.file)
		byPath[path] = append(byPath[path], e.diag)
	}
	return byPath, nil
}
