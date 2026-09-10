package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// AllTools returns every tool definition.
func AllTools() []ToolDef {
	return []ToolDef{
		{
			Name: "read_file",
			Description: "Read a file, or a range of its lines via start_line/end_line. " +
				"Use this instead of shelling out to sed/head/tail: it reads the live editor buffer, so it shows unsaved edits that the on-disk file does not have, and a ranged read reports the line numbers and the file's total length.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path":       {Type: "string", Description: "Path to the file, relative to workspace root or absolute."},
					"start_line": {Type: "integer", Description: "1-based first line to return. Omit to start at the beginning."},
					"end_line":   {Type: "integer", Description: "1-based last line to return, inclusive. Omit to read to the end."},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List files and directories inside a directory.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path":      {Type: "string", Description: "Directory path, relative to workspace root. Defaults to workspace root if empty."},
					"recursive": {Type: "string", Description: "Set to 'true' to list recursively (depth-limited). Default is shallow."},
				},
			},
		},
		{
			Name:        "search_files",
			Description: "Search for a pattern across files in the workspace using grep. Returns matching lines with file paths and line numbers.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"pattern": {Type: "string", Description: "The search pattern (regular expression)."},
					"path":    {Type: "string", Description: "Limit search to this directory (relative to workspace root). Defaults to all files."},
					"include": {Type: "string", Description: "Glob to restrict file types, e.g. '*.go'."},
				},
				Required: []string{"pattern"},
			},
		},
		{
			Name:        "apply_edits",
			Description: "Apply a text edit to a file. Replaces old_text with new_text. The user will see a diff and must approve before any change is made.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path":     {Type: "string", Description: "File to edit, relative to workspace root or absolute."},
					"reason":   {Type: "string", Description: "Short explanation of why this edit is needed."},
					"old_text": {Type: "string", Description: "The exact text to replace (must match exactly, including whitespace)."},
					"new_text": {Type: "string", Description: "The replacement text."},
				},
				Required: []string{"path", "reason", "old_text", "new_text"},
			},
		},
		{
			Name:        "insert_at_line",
			Description: "Insert new line(s) at an exact 1-based line number: the inserted text becomes that line and existing lines shift down. Preferred over apply_edits when the user names a line number or says 'at the cursor'. The user must approve before any change is made.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path":   {Type: "string", Description: "File to edit, relative to workspace root or absolute."},
					"reason": {Type: "string", Description: "Short explanation of why this edit is needed."},
					"line":   {Type: "integer", Description: "1-based line number the inserted text should end up on."},
					"text":   {Type: "string", Description: "The line(s) to insert."},
				},
				Required: []string{"path", "reason", "line", "text"},
			},
		},
		{
			Name:        "goto_file",
			Description: "Navigate the user's editor window to a file (and optionally a line), so they can see it directly instead of just reading a path in chat. Use this after locating where something is implemented, e.g. in response to 'take me to X' or 'where is X handled'.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {Type: "string", Description: "File to open, relative to workspace root or absolute."},
					"line": {Type: "integer", Description: "1-based line number to jump to. Omit or 0 to just open the file."},
				},
				Required: []string{"path"},
			},
		},
		{
			Name: "get_diagnostics",
			Description: "Errors and warnings for a file, from its language server and any configured linters, computed against the live buffer including unsaved edits. " +
				"Far cheaper than running a build to find out whether an edit compiles, and it reports column-accurate positions. " +
				"An empty result with lsp_ready=false means the language server is still starting — retry rather than concluding the file is clean.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {Type: "string", Description: "Path to the file, relative to workspace root or absolute."},
				},
				Required: []string{"path"},
			},
		},
		{
			Name: "get_workspace_diagnostics",
			Description: "Errors and warnings across the whole project, not just one file — use it after a change that could break callers elsewhere, instead of running a full build. " +
				"Coverage differs by file: files open in the editor get live language-server and linter results, while files that are not open get only whatever the last whole-project linter scan found. " +
				"So pass rescan=true after editing to refresh the unopened files; without it, a clean result is not evidence that the project builds.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"rescan": {Type: "string", Description: "Set to 'true' to start a fresh whole-project linter scan first and wait briefly for results. Slower, but required for the answer to reflect edits to files that aren't open."},
				},
			},
		},
		{
			Name: "find_definition",
			Description: "Where a symbol is defined, from the language server. " +
				"This is the tool for \"where is X defined?\" — pass symbol alone, nothing else. " +
				"Prefer it over grepping for a name: it resolves the actual binding, so it does not match comments, strings, or unrelated identifiers that happen to share the name.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"symbol": {Type: "string", Description: "Name of the symbol, e.g. \"queueWorkItemUpdate\". The usual way to call this: no other argument is needed."},
					"path":   {Type: "string", Description: "File containing the symbol. Only needed to disambiguate, or when using line/col."},
					"line":   {Type: "string", Description: "1-based line number of the symbol. Alternative to symbol, when you already know the position."},
					"col":    {Type: "string", Description: "1-based column of the symbol. Defaults to the first non-space character on the line."},
				},
			},
		},
		{
			Name: "find_references",
			Description: "Every place a symbol is used, from the language server, each with a source-line preview. " +
				"This is the tool for \"what calls X?\" — pass symbol alone, nothing else. " +
				"Unlike a text search it follows the real binding, so it finds usages a grep for the bare name would miss (renamed imports, method values) and skips ones it would wrongly match (comments, strings, unrelated same-named things). " +
				"Give path/line/col instead only to disambiguate a name that several things share.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"symbol": {Type: "string", Description: "Name of the symbol, e.g. \"updateWorkItem\". The usual way to call this: no other argument is needed."},
					"path":   {Type: "string", Description: "File containing the symbol. Only needed to disambiguate, or when using line/col."},
					"line":   {Type: "string", Description: "1-based line number of the symbol. Alternative to symbol, when you already know the position."},
					"col":    {Type: "string", Description: "1-based column of the symbol. Defaults to the first non-space character on the line."},
				},
			},
		},
		{
			Name: "list_symbols",
			Description: "Symbols defined in a file (its outline), or — when query is also given — a fuzzy search for that name across every file the language server indexes. " +
				"Use it to locate a function or type by name instead of guessing which file holds it.",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path":  {Type: "string", Description: "File to outline. Required for an outline; optional when searching by query, where it only narrows which language is searched."},
					"query": {Type: "string", Description: "Symbol name to search for across the workspace. Give this alone to find where something lives without knowing any path."},
				},
			},
		},
		{
			Name:        "save_file",
			Description: "Write a file's live editor buffer to disk. Approved edits apply to the buffer immediately but the on-disk file stays stale until saved — call this on every file you edited before running disk-based commands (builds, tests, grep). If the file is open in the editor the user is asked to approve the save (it may include their own unsaved changes).",
			InputSchema: ToolSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"path": {Type: "string", Description: "File to save, relative to workspace root or absolute."},
				},
				Required: []string{"path"},
			},
		},
	}
}

// ─── input types ─────────────────────────────────────────────────────────────

type readFileInput struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}
type listFilesInput struct {
	Path      string `json:"path"`
	Recursive string `json:"recursive"`
}
type searchFilesInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
	Include string `json:"include"`
}
type applyEditsInput struct {
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}
type insertAtLineInput struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
}
type gotoFileInput struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// ─── dispatcher ───────────────────────────────────────────────────────────────

// ExecTool runs one tool by name against a live indigo server. It returns
// the text result and whether that result is an error.
func ExecTool(ctx context.Context, rpc *client.RPC, ap Approver, workDir, name string, rawInput json.RawMessage) (string, bool) {
	switch name {
	case "read_file":
		var in readFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execReadFile(ctx, rpc, workDir, in)
	case "list_files":
		var in listFilesInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execListFiles(workDir, in.Path, in.Recursive == "true")
	case "search_files":
		var in searchFilesInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execSearchFiles(workDir, in.Pattern, in.Path, in.Include)
	case "apply_edits":
		var in applyEditsInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execApplyEdits(ctx, rpc, ap, workDir, in)
	case "insert_at_line":
		var in insertAtLineInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execInsertAtLine(ctx, rpc, ap, workDir, in)
	case "goto_file":
		var in gotoFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execGotoFile(ctx, rpc, workDir, in)
	case "get_diagnostics":
		var in readFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execGetDiagnostics(ctx, rpc, workDir, in.Path)
	case "get_workspace_diagnostics":
		var in workspaceDiagnosticsInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execGetWorkspaceDiagnostics(ctx, rpc, workDir, in)
	case "find_definition":
		var in symbolPosInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execFindDefinition(ctx, rpc, workDir, in)
	case "find_references":
		var in symbolPosInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execFindReferences(ctx, rpc, workDir, in)
	case "list_symbols":
		var in listSymbolsInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execListSymbols(ctx, rpc, workDir, in)
	case "save_file":
		var in readFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execSaveFile(ctx, rpc, ap, workDir, in.Path)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}
}

// ─── read_file ────────────────────────────────────────────────────────────────

func execReadFile(ctx context.Context, rpc *client.RPC, workDir string, in readFileInput) (string, bool) {
	abs := absPath(workDir, in.Path)
	content, fromDisk, err := readWholeFile(ctx, rpc, abs)
	if err != nil {
		return fmt.Sprintf("cannot read %s: %v", in.Path, err), true
	}
	out, isErr := sliceLines(content, in)
	if fromDisk && !isErr {
		out = diskFallbackNote + out
	}
	return out, isErr
}

// diskFallbackNote marks content that did not come from the editor buffer.
//
// The silent version of this fallback was actively harmful: with the server
// unreachable, read_file returned on-disk bytes that looked exactly like a
// normal answer, so an agent would reason from stale content while believing
// it was seeing unsaved edits — the one guarantee that makes this tool worth
// choosing over `cat`. Saying so costs a line and makes the degradation
// visible.
const diskFallbackNote = "NOTE: read from disk, not the editor buffer — the indigo server for this " +
	"workspace could not be reached, so any unsaved edits are NOT reflected below.\n\n"

// readWholeFile returns a file's content, reporting whether it had to come
// from disk because the server was unreachable.
func readWholeFile(ctx context.Context, rpc *client.RPC, abs string) (content string, fromDisk bool, err error) {
	bufID, content, _, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		data, ferr := os.ReadFile(abs)
		if ferr != nil {
			return "", false, ferr
		}
		return string(data), true, nil
	}
	count, cerr := rpc.BufferClientCount(ctx, bufID)
	if cerr == nil && count == 1 {
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
	}
	return content, false, nil
}

// sliceLines applies the optional start_line/end_line window.
//
// Without a window the content is returned byte-for-byte, which matters: the
// text an agent reads here is the text it will hand back as apply_edits'
// old_text, so read_file's default output must stay copyable verbatim. A
// windowed read instead gets one header line naming the range and the file's
// total length — that anchor is what lets a finding be cited as file:line
// without counting, and it is the reason `sed -n '95,140p'` looked cheaper
// than this tool for reading part of a large file.
func sliceLines(content string, in readFileInput) (string, bool) {
	if in.StartLine <= 0 && in.EndLine <= 0 {
		return content, false
	}
	lines := strings.Split(content, "\n")
	// A trailing newline yields a final empty element that is not a line.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)

	start, end := in.StartLine, in.EndLine
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	if start > total {
		return fmt.Sprintf("%s has %d line(s); start_line %d is past the end",
			in.Path, total, in.StartLine), true
	}
	if start > end {
		return fmt.Sprintf("start_line %d is after end_line %d", start, end), true
	}
	return fmt.Sprintf("%s lines %d-%d (of %d):\n%s",
		in.Path, start, end, total, strings.Join(lines[start-1:end], "\n")), false
}

// ─── list_files ───────────────────────────────────────────────────────────────

const maxListDepth = 5

func execListFiles(workDir, relPath string, recursive bool) (string, bool) {
	dir := workDir
	if relPath != "" {
		dir = absPath(workDir, relPath)
	}
	var sb strings.Builder
	if recursive {
		if err := listRecursive(&sb, dir, workDir, 0); err != nil {
			return fmt.Sprintf("cannot list %s: %v", relPath, err), true
		}
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Sprintf("cannot list %s: %v", relPath, err), true
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				name += "/"
			}
			sb.WriteString(name + "\n")
		}
	}
	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return "(empty directory)", false
	}
	return out, false
}

func listRecursive(sb *strings.Builder, dir, workDir string, depth int) error {
	if depth > maxListDepth {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		rel, _ := filepath.Rel(workDir, filepath.Join(dir, e.Name()))
		name := rel
		if e.IsDir() {
			name += "/"
		}
		sb.WriteString(strings.Repeat("  ", depth) + name + "\n")
		if e.IsDir() {
			listRecursive(sb, filepath.Join(dir, e.Name()), workDir, depth+1) //nolint:errcheck
		}
	}
	return nil
}

// ─── search_files ─────────────────────────────────────────────────────────────

func execSearchFiles(workDir, pattern, relPath, include string) (string, bool) {
	searchDir := workDir
	if relPath != "" {
		searchDir = absPath(workDir, relPath)
	}

	var out []byte
	var err error

	if isGitRepo(workDir) {
		// -e keeps pattern a search expression: without it a pattern starting
		// with "-" is parsed as an option, so searching for something like
		// "--force" fails with a git usage error instead of searching. The
		// plain-grep branch below gets the same protection from its "--".
		args := []string{"-C", workDir, "grep", "-n", "-e", pattern}
		if include != "" {
			args = append(args, "--", "*."+strings.TrimPrefix(include, "*."))
		} else if relPath != "" {
			args = append(args, "--", relPath)
		}
		out, err = exec.Command("git", args...).Output()
	} else {
		args := []string{"-rn"}
		if include != "" {
			args = append(args, "--include="+include)
		}
		args = append(args, "--", pattern, searchDir)
		out, err = exec.Command("grep", args...).Output()
	}

	if err != nil && len(out) == 0 {
		return "(no matches)", false
	}
	result := strings.TrimRight(string(out), "\n")
	if result == "" {
		return "(no matches)", false
	}
	const maxBytes = 32 * 1024
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n… (truncated)"
	}
	return result, false
}

func isGitRepo(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// requestEditApproval asks the front end whether a change may proceed.
//
// It used to own an in-editor approval popup and the auto-approve check
// directly; both moved behind Approver when this package was split out of the
// chat plugin, so a front end with no UI can answer without the plumbing of
// one.
func requestEditApproval(ap Approver, req EditRequest) bool {
	return ap.ApproveEdit(req)
}

// ─── apply_edits ──────────────────────────────────────────────────────────────

func execApplyEdits(ctx context.Context, rpc *client.RPC, ap Approver, workDir string, in applyEditsInput) (string, bool) {
	abs := absPath(workDir, in.Path)

	if !requestEditApproval(ap, EditRequest{
		File:   in.Path,
		Reason: in.Reason,
		Edits:  []EditSpec{{Path: abs, OldText: in.OldText, NewText: in.NewText}},
	}) {
		return "edit rejected by user", true
	}

	// Open the buffer (idempotent if already open).
	bufID, content, version, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		return fmt.Sprintf("cannot open %s: %v", in.Path, err), true
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	weOpened := count == 1

	idx := strings.Index(content, in.OldText)
	if idx == -1 {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("old_text not found in %s", in.Path), true
	}

	startLine, startCol := offsetToLineCol(content, idx)
	endLine, endCol := offsetToLineCol(content, idx+len(in.OldText))

	// One atomic batch: the server applies both ops even if this process dies
	// mid-call, so the buffer can never be left with the deletion but not the
	// replacement text.
	if _, err := rpc.ApplyOps(ctx, bufID, []document.Op{
		{
			Type:     document.OpDelete,
			FromLine: startLine,
			FromCol:  startCol,
			ToLine:   endLine,
			ToCol:    endCol,
			Version:  version,
		},
		{
			Type:       document.OpInsert,
			InsertLine: startLine,
			InsertCol:  startCol,
			InsertText: in.NewText,
		},
	}); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("edit ops failed: %v", err), true
	}

	if weOpened {
		if serr := rpc.Save(ctx, bufID); serr != nil {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
			return fmt.Sprintf("edited %s (save failed: %v)", in.Path, serr), false
		}
		warn := verifySaved(ctx, rpc, bufID, abs)
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		return fmt.Sprintf("edited and saved %s%s", abs, warn), warn != ""
	}
	return fmt.Sprintf("edited %s — applied to the live buffer (approved by the user). Not yet on disk: call save_file before disk-based builds/tests.", in.Path), false
}

// ─── insert_at_line ───────────────────────────────────────────────────────────

// insertLineOp returns the insert op that makes text become 1-based line
// `line` of content, shifting existing lines down. A line past the end of the
// file appends after the last line instead.
func insertLineOp(content, text string, line int) document.Op {
	if line < 1 {
		line = 1
	}
	total := strings.Count(content, "\n") + 1
	if line <= total {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		return document.Op{Type: document.OpInsert, InsertLine: line - 1, InsertCol: 0, InsertText: text}
	}
	lines := strings.Split(content, "\n")
	last := len(lines) - 1
	return document.Op{
		Type:       document.OpInsert,
		InsertLine: last,
		InsertCol:  len([]rune(lines[last])),
		InsertText: "\n" + strings.TrimSuffix(text, "\n"),
	}
}

func execInsertAtLine(ctx context.Context, rpc *client.RPC, ap Approver, workDir string, in insertAtLineInput) (string, bool) {
	abs := absPath(workDir, in.Path)

	if !requestEditApproval(ap, EditRequest{
		File:   fmt.Sprintf("%s (insert at line %d)", in.Path, in.Line),
		Reason: in.Reason,
		Edits:  []EditSpec{{Path: abs, OldText: "", NewText: in.Text}},
	}) {
		return "edit rejected by user", true
	}

	bufID, content, _, _, generation, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		return fmt.Sprintf("cannot open %s: %v", in.Path, err), true
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	weOpened := count == 1

	if _, err := rpc.ApplyOp(ctx, bufID, insertLineOp(content, in.Text, in.Line), generation); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("insert op failed: %v", err), true
	}

	if weOpened {
		if serr := rpc.Save(ctx, bufID); serr != nil {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
			return fmt.Sprintf("inserted at line %d in %s (save failed: %v)", in.Line, in.Path, serr), false
		}
		// Read the file back like execApplyEdits and execSaveFile do, rather
		// than trusting Save's return: reporting a write that did not reach
		// disk as success is the specific failure this project has already
		// been burned by (see CLAUDE.md's "Known tooling issue").
		warn := verifySaved(ctx, rpc, bufID, absPath(workDir, in.Path))
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		return fmt.Sprintf("inserted at line %d and saved %s%s", in.Line, in.Path, warn), warn != ""
	}
	return fmt.Sprintf("inserted at line %d in %s — applied to the live buffer (approved by the user). Not yet on disk: call save_file before disk-based builds/tests.", in.Line, in.Path), false
}

// ─── goto_file ────────────────────────────────────────────────────────────────

// gotoFileWireLine converts a 1-based line from tool input to the 0-based
// convention RequestOpenFile uses over the wire. 0 or negative (omitted)
// becomes 0 — the top of the file, since there's no "no line" sentinel on
// the wire.
func gotoFileWireLine(oneBased int) uint32 {
	if oneBased > 0 {
		return uint32(oneBased - 1)
	}
	return 0
}

// execGotoFile asks every connected editor client to navigate to path (and
// optionally a 1-based line), so the user sees the location directly rather
// than just reading a path in the chat transcript. No approval popup: this
// only moves the cursor, it never changes file content.
func execGotoFile(ctx context.Context, rpc *client.RPC, workDir string, in gotoFileInput) (string, bool) {
	abs := absPath(workDir, in.Path)
	if _, err := os.Stat(abs); err != nil {
		return fmt.Sprintf("cannot find %s: %v", in.Path, err), true
	}
	if err := rpc.RequestOpenFile(ctx, abs, gotoFileWireLine(in.Line)); err != nil {
		return fmt.Sprintf("could not navigate to %s: %v", in.Path, err), true
	}
	if in.Line > 0 {
		return fmt.Sprintf("opened %s at line %d in the user's editor", in.Path, in.Line), false
	}
	return fmt.Sprintf("opened %s in the user's editor", in.Path), false
}

// ─── save_file ────────────────────────────────────────────────────────────────

// execSaveFile writes the live buffer for path to disk so disk-based tools
// (builds, tests, grep) see edits that were applied to the buffer. When the
// buffer is open in an editor it may hold the user's own unsaved changes, so
// the save must be approved; when only the agent has it open, every unsaved
// byte was already approved edit by edit and the save is silent.
func execSaveFile(ctx context.Context, rpc *client.RPC, ap Approver, workDir, path string) (string, bool) {
	abs := absPath(workDir, path)
	bufID, _, _, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		return fmt.Sprintf("cannot open %s: %v", path, err), true
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	weOpened := count == 1

	if !weOpened {
		if !requestEditApproval(ap, EditRequest{
			File:   path,
			Reason: "Save buffer to disk so builds/tests see the edits. Any of your own unsaved changes in this file will be saved too.",
		}) {
			return "save rejected by user — on-disk file unchanged; buffer edits remain applied in the editor", true
		}
	}

	if err := rpc.Save(ctx, bufID); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("save failed for %s: %v", path, err), true
	}
	warn := verifySaved(ctx, rpc, bufID, abs)
	if weOpened {
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
	}
	return fmt.Sprintf("saved %s to disk%s", abs, warn), warn != ""
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func absPath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

// offsetToLineCol converts a byte offset into s (what strings.Index returns)
// into the line and column document.Op expects.
//
// The column is counted in **runes**, not bytes. document.Buffer addresses
// positions logically — logicalOffset does lineStart+col over a rune sequence,
// and logicalSlice slices runes — so a byte column silently points somewhere
// else in any line containing multibyte text. Editing a line holding "héllo"
// deleted from the wrong column and corrupted the text rather than failing.
func offsetToLineCol(s string, byteOffset int) (line, col int) {
	if byteOffset > len(s) {
		byteOffset = len(s)
	}
	if byteOffset < 0 {
		byteOffset = 0
	}
	// Ranging over a string yields runes, so col advances once per rune
	// regardless of how many bytes it occupies.
	for _, r := range s[:byteOffset] {
		if r == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}

// verifySaved confirms the bytes actually reached disk after a Save, by
// reading the file back and comparing it to the buffer the server holds.
//
// This exists because of a reported failure that could not be reproduced:
// apply_edits/save_file returned success, a follow-up read_file showed the
// edit (it reads the live buffer), and yet the file on disk was unchanged
// (CLAUDE.md, 2026-08-17). Driving the server's own
// OpenFile/ApplyOps/Save/CloseBuffer sequence proves that path writes
// correctly, so whatever happened was above or around it — a stale plugin
// build, a second buffer for the same file, or something else not yet found.
//
// Rather than leave the root cause open *and* the symptom silent, this makes
// the bad state unreportable: an agent is told the save did not land instead
// of being told it succeeded, so it can react rather than building on a file
// it wrongly believes it changed. It returns the mismatch as a message, never
// an error, since the buffer edit itself did succeed.
func verifySaved(ctx context.Context, rpc *client.RPC, bufID uint32, abs string) string {
	want, _, _, _, err := rpc.GetBufferSnapshot(ctx, bufID)
	return verifySavedAgainst(want, err, abs)
}

// verifySavedAgainst is verifySaved's comparison, split out so it can be
// tested without a live server: it takes the buffer content the server
// reported (and any error fetching it) rather than fetching it itself.
func verifySavedAgainst(want string, snapErr error, abs string) string {
	if snapErr != nil {
		// Can't verify; say so rather than implying it was checked.
		return fmt.Sprintf(" (could not verify the write: %v)", snapErr)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf(" — WARNING: the file could not be read back after saving (%v); "+
			"do not assume the change is on disk", err)
	}
	if string(got) != want {
		return fmt.Sprintf(" — WARNING: %s on disk does not match the buffer after saving. "+
			"The edit is applied in the editor but the file has NOT changed on disk; "+
			"disk-based builds, tests and greps will not see it.", abs)
	}
	return ""
}

// ─── LSP tools ────────────────────────────────────────────────────────────────
//
// These exist because the agent's alternative is grep. The language server
// already running for this workspace knows what a name actually binds to and
// where the errors are, and every one of these reads the *live buffer*, so
// results reflect unsaved edits rather than what happens to be on disk.
//
// All four are read-only: they never touch a buffer or the filesystem.

type symbolPosInput struct {
	Symbol string `json:"symbol"`
	Path   string `json:"path"`
	Line   string `json:"line"`
	Col    string `json:"col"`
}

type listSymbolsInput struct {
	Path  string `json:"path"`
	Query string `json:"query"`
}

// openForRead opens path for a read-only query and returns a closer that
// releases it again if we were the one who opened it. Leaving buffers open
// behind a query would keep the server's history alive and, worse, make the
// *next* tool call think a second client has the file open.
func openForRead(ctx context.Context, rpc *client.RPC, workDir, path string) (bufID uint32, content string, done func(), err error) {
	abs := absPath(workDir, path)
	bufID, content, _, _, _, err = rpc.OpenFile(ctx, abs)
	if err != nil {
		return 0, "", func() {}, fmt.Errorf("cannot open %s: %w", path, err)
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	if count == 1 {
		id := bufID
		return bufID, content, func() { rpc.CloseBuffer(ctx, id) }, nil //nolint:errcheck
	}
	return bufID, content, func() {}, nil
}

// resolvePos turns the tool's 1-based line/col into the 0-based pair the RPC
// layer uses. An omitted column lands on the line's first non-space character,
// which is almost always the symbol the caller means and saves them counting.
func resolvePos(content string, in symbolPosInput) (line, col int, err error) {
	line, err = strconv.Atoi(strings.TrimSpace(in.Line))
	if err != nil || line < 1 {
		return 0, 0, fmt.Errorf("line must be a positive integer, got %q", in.Line)
	}
	line--

	if c := strings.TrimSpace(in.Col); c != "" {
		col, err = strconv.Atoi(c)
		if err != nil || col < 1 {
			return 0, 0, fmt.Errorf("col must be a positive integer, got %q", in.Col)
		}
		return line, col - 1, nil
	}

	lines := strings.Split(content, "\n")
	if line < len(lines) {
		for i, r := range lines[line] {
			if r != ' ' && r != '\t' {
				return line, i, nil
			}
		}
	}
	return line, 0, nil
}

func severityLabel(s uint8) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	default:
		return "hint"
	}
}

// diagnosticsWait bounds how long get_diagnostics will wait for a language
// server to produce its first results.
//
// This is load-bearing rather than a convenience. LspReady only reports that a
// server *process* exists for the file type, not that it has analysed
// anything, so a freshly started gopls answers "ready, zero diagnostics" for a
// file with an outright type error. The editor never notices — diagnostics
// stream in over its tick loop and the UI catches up — but a one-shot tool
// call has no second chance, and "no diagnostics" reads as "this compiles".
//
// So an empty result is retried until something arrives or the budget runs
// out. A file with problems usually answers in well under a second; only a
// genuinely clean file (or a cold index) pays the full wait.
const (
	diagnosticsWait = 5 * time.Second
	diagnosticsPoll = 200 * time.Millisecond
)

// waitForDiagnostics polls until diagnostics appear, the budget expires, or
// the context ends. It returns the last result either way, so callers always
// have LspReady to interpret an empty list with.
// hasErrorOrWarning reports whether any diagnostic is severity error (1) or
// warning (2), as opposed to info/hint from a linter or spell-checker.
func hasErrorOrWarning(diags []client.ClientDiag) bool {
	for _, d := range diags {
		if d.Severity == 1 || d.Severity == 2 {
			return true
		}
	}
	return false
}

func waitForDiagnostics(ctx context.Context, rpc *client.RPC, bufID uint32) (client.DiagnosticsResult, error) {
	deadline := time.Now().Add(diagnosticsWait)
	for {
		res, err := rpc.GetDiagnostics(ctx, bufID)
		if err != nil {
			return res, err
		}
		// Return early only once something that matters has arrived. Returning
		// on *any* diagnostic looked right but wasn't: a fast source (the
		// spell-check plugin) answers almost immediately, so the poll would
		// stop with a list of hints while the compiler error an agent is
		// actually asking about was still seconds away.
		if hasErrorOrWarning(res.Diags) || !res.LspReady || time.Now().After(deadline) {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, nil
		case <-time.After(diagnosticsPoll):
		}
	}
}

func execGetDiagnostics(ctx context.Context, rpc *client.RPC, workDir, path string) (string, bool) {
	bufID, _, done, err := openForRead(ctx, rpc, workDir, path)
	if err != nil {
		return err.Error(), true
	}
	defer done()

	res, err := waitForDiagnostics(ctx, rpc, bufID)
	if err != nil {
		return fmt.Sprintf("diagnostics failed for %s: %v", path, err), true
	}
	if len(res.Diags) == 0 {
		if !res.LspReady {
			// No language server at all for this file type — say so rather
			// than implying the file was checked and found clean.
			return fmt.Sprintf("no diagnostics for %s: no language server is running for this "+
				"file type, so nothing analysed it", path), false
		}
		return fmt.Sprintf("no diagnostics for %s (checked over %s; the language server was "+
			"running and reported nothing)", path, diagnosticsWait), false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d diagnostic(s) for %s:\n", len(res.Diags), path)
	for _, d := range res.Diags {
		src := d.Source
		if src == "" {
			src = "lsp"
		}
		fmt.Fprintf(&b, "  %s:%d:%d %s: %s [%s]\n",
			path, d.Line+1, d.Col+1, severityLabel(d.Severity), d.Message, src)
	}
	if !res.LspReady {
		b.WriteString("  (language server still starting — the list may be incomplete)\n")
	}
	return b.String(), false
}

func execFindDefinition(ctx context.Context, rpc *client.RPC, workDir string, in symbolPosInput) (string, bool) {
	var note string
	var resolved *client.ClientSymbol
	if in.Symbol != "" && in.Line == "" {
		// Same entry point find_references has: a caller who knows only a name
		// must not have to locate it first. Requiring a path here is what left
		// `grep -rn <name>` as the only way to answer "where is X defined?"
		// from a cold start, which is both slower and textual.
		sym, others, err := resolveSymbol(ctx, rpc, workDir, in.Symbol, in.Path)
		if err != nil {
			return err.Error(), true
		}
		note = ambiguityNote(in.Symbol, sym, others)
		resolved = &sym
		in.Path, in.Line, in.Col = sym.Path, strconv.Itoa(sym.Line+1), strconv.Itoa(sym.Col+1)
	}
	if in.Path == "" || in.Line == "" {
		return "give either symbol (the usual case: the name alone) or path and line", true
	}

	bufID, content, done, err := openForRead(ctx, rpc, workDir, in.Path)
	if err != nil {
		return err.Error(), true
	}
	defer done()

	line, col, err := resolvePos(content, in)
	if err != nil {
		return err.Error(), true
	}
	loc, found, err := rpc.Definition(ctx, bufID, line, col)
	if err != nil {
		return fmt.Sprintf("definition lookup failed: %v", err), true
	}
	if !found {
		// Asking a language server to go-to-definition while already sitting on
		// the declaration legitimately returns nothing on some servers. When we
		// got here by resolving a name, that resolution *is* the declaration —
		// reporting "not found" would throw away an answer we already have.
		if resolved != nil {
			return note + fmt.Sprintf("%s:%d:%d", resolved.Path, resolved.Line+1, resolved.Col+1), false
		}
		return fmt.Sprintf("no definition found at %s:%d:%d", in.Path, line+1, col+1), false
	}
	return note + fmt.Sprintf("%s:%d:%d", loc.Path, loc.Line+1, loc.Col+1), false
}

// ambiguityNote reports the other symbols sharing a name when a bare-name
// lookup had to choose between them. Silently picking one is exactly the case
// where a wrong answer looks right, so the choice is always stated.
func ambiguityNote(symbol string, chosen client.ClientSymbol, others []client.ClientSymbol) string {
	if len(others) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "note: %d symbols are named %q; using %s:%d. Others:\n",
		len(others)+1, symbol, chosen.Path, chosen.Line+1)
	for _, o := range others {
		fmt.Fprintf(&b, "  %s:%d:%d\n", o.Path, o.Line+1, o.Col+1)
	}
	b.WriteString("Pass path to pick a different one.\n")
	return b.String()
}

func execFindReferences(ctx context.Context, rpc *client.RPC, workDir string, in symbolPosInput) (string, bool) {
	var note string
	if in.Symbol != "" && in.Line == "" {
		// Resolve the name to a position first, so a caller who knows only
		// "what calls X?" never has to locate X themselves. Needing a path
		// before this tool would do anything is what made grep the cheaper
		// option and kept it from being used at all.
		sym, others, err := resolveSymbol(ctx, rpc, workDir, in.Symbol, in.Path)
		if err != nil {
			return err.Error(), true
		}
		note = ambiguityNote(in.Symbol, sym, others)
		in.Path, in.Line, in.Col = sym.Path, strconv.Itoa(sym.Line+1), strconv.Itoa(sym.Col+1)
	}
	if in.Path == "" || in.Line == "" {
		return "give either symbol (the usual case: the name alone) or path and line", true
	}

	bufID, content, done, err := openForRead(ctx, rpc, workDir, in.Path)
	if err != nil {
		return err.Error(), true
	}
	defer done()

	line, col, err := resolvePos(content, in)
	if err != nil {
		return err.Error(), true
	}
	refs, err := rpc.References(ctx, bufID, line, col)
	if err != nil {
		return fmt.Sprintf("references lookup failed: %v", err), true
	}
	if len(refs) == 0 {
		return note + fmt.Sprintf("no references found at %s:%d:%d", in.Path, line+1, col+1), false
	}

	fillPreviews(ctx, rpc, refs)

	var b strings.Builder
	b.WriteString(note)
	fmt.Fprintf(&b, "%d reference(s):\n", len(refs))
	for _, r := range refs {
		if p := strings.TrimSpace(r.Preview); p != "" {
			fmt.Fprintf(&b, "  %s:%d:%d  %s\n", r.Path, r.Line+1, r.Col+1, p)
		} else {
			fmt.Fprintf(&b, "  %s:%d:%d\n", r.Path, r.Line+1, r.Col+1)
		}
	}
	return b.String(), false
}

// previewFileBudget caps how many distinct files fillPreviews will open. A
// reference list spanning a handful of files is the normal case and worth the
// opens; one spanning hundreds is not, and the locations alone are still
// useful.
const previewFileBudget = 20

// fillPreviews supplies the source line for references the server left blank.
//
// The server only previews hits in the file that was queried, because that is
// the only buffer it knows is open — so a cross-file result, which is the
// common and most useful case, arrives as bare locations. Without the source
// line the agent has to read every hit to find out which ones matter, which
// makes the tool barely better than a grep.
//
// Previews are read through the buffer rather than off disk so they show
// unsaved edits, consistent with every other tool here.
func fillPreviews(ctx context.Context, rpc *client.RPC, refs []client.ClientReference) {
	byFile := map[string][]int{}
	for i, r := range refs {
		if strings.TrimSpace(r.Preview) != "" {
			continue
		}
		byFile[r.Path] = append(byFile[r.Path], i)
	}

	opened := 0
	for path, idxs := range byFile {
		if opened >= previewFileBudget {
			return
		}
		opened++

		bufID, content, done, err := openForRead(ctx, rpc, "", path)
		if err != nil {
			continue // a preview is a nicety; never fail the lookup over one
		}
		_ = bufID
		lines := strings.Split(content, "\n")
		for _, i := range idxs {
			if ln := refs[i].Line; ln >= 0 && ln < len(lines) {
				refs[i].Preview = lines[ln]
			}
		}
		done()
	}
}

func execListSymbols(ctx context.Context, rpc *client.RPC, workDir string, in listSymbolsInput) (string, bool) {
	// A path is required even for a workspace-wide query, and not as an
	// arbitrary restriction: LSP's workspace/symbol is answered by one
	// language server, so something has to say which. Any file in the target
	// language does.
	//
	// An earlier version tried to avoid asking by opening "." as a stand-in,
	// which fails outright — the server cannot open a directory as a buffer,
	// so every query-only search returned "is a directory".
	openPath := in.Path
	if openPath == "" {
		if in.Query == "" {
			return "give path (to outline a file) or query (to search the workspace by name)", true
		}
		// A workspace query still needs *some* buffer open for the server to
		// route to a language server, but the caller should not have to supply
		// one: which language to search is a property of the workspace, not of
		// the question. Requiring it here is what made this tool unreachable
		// from a bare symbol name.
		cands := representativeFiles(workDir)
		if len(cands) == 0 {
			return "no file with a configured language server found in this workspace; " +
				"pass path to say which language to search", true
		}
		openPath = cands[0]
	}

	bufID, _, done, err := openForRead(ctx, rpc, workDir, openPath)
	if err != nil {
		return err.Error(), true
	}
	defer done()

	var syms []client.ClientSymbol
	if in.Query != "" {
		syms, err = rpc.WorkspaceSymbols(ctx, bufID, in.Query)
	} else {
		syms, err = rpc.DocumentSymbols(ctx, bufID)
	}
	if err != nil {
		return fmt.Sprintf("symbol lookup failed: %v", err), true
	}
	if len(syms) == 0 {
		if in.Query != "" {
			return fmt.Sprintf("no symbols matching %q", in.Query), false
		}
		return fmt.Sprintf("no symbols in %s", in.Path), false
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d symbol(s):\n", len(syms))
	for _, sy := range syms {
		name := sy.Name
		if sy.ContainerName != "" {
			name = sy.ContainerName + "." + name
		}
		fmt.Fprintf(&b, "  %-4s %s  %s:%d:%d\n", sy.KindLabel, name, sy.Path, sy.Line+1, sy.Col+1)
	}
	return b.String(), false
}

// ─── workspace diagnostics ────────────────────────────────────────────────────

type workspaceDiagnosticsInput struct {
	Rescan string `json:"rescan"`
}

// workspaceRescanWait bounds how long a rescan waits for results.
//
// Longer than the single-file budget because a whole-project scan shells out
// to a real linter (golangci-lint, eslint, ruff, clippy) over every file,
// which takes seconds on any non-trivial repo. Returns as soon as an error or
// a warning appears, for the same reason the single-file poll does: fast
// sources answer first, and stopping on their info-level output would hide
// the compile error the caller is actually asking about.
const workspaceRescanWait = 15 * time.Second

func execGetWorkspaceDiagnostics(ctx context.Context, rpc *client.RPC, workDir string, in workspaceDiagnosticsInput) (string, bool) {
	rescan := strings.EqualFold(strings.TrimSpace(in.Rescan), "true")

	if rescan {
		if err := rpc.RescanWorkspaceDiagnostics(ctx); err != nil {
			return fmt.Sprintf("could not start a workspace scan: %v", err), true
		}
	}

	res, err := rpc.GetWorkspaceDiagnostics(ctx)
	if err != nil {
		return fmt.Sprintf("workspace diagnostics failed: %v", err), true
	}
	if rescan {
		// The scan is fire-and-forget on the server, so there is no completion
		// signal to wait on — poll until something worth reporting shows up or
		// the budget runs out.
		deadline := time.Now().Add(workspaceRescanWait)
		for !workspaceHasErrorOrWarning(res.Items) && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				// Return, not break: a bare break here leaves the select,
				// not the loop, so a cancelled context would keep polling
				// until the deadline (SA4011).
				return renderWorkspaceDiagnostics(res, workDir, rescan), false
			case <-time.After(diagnosticsPoll):
			}
			if r, err := rpc.GetWorkspaceDiagnostics(ctx); err == nil {
				res = r
			}
		}
	}

	return renderWorkspaceDiagnostics(res, workDir, rescan), false
}

// renderWorkspaceDiagnostics formats a workspace diagnostics result.
func renderWorkspaceDiagnostics(res client.WorkspaceDiagnosticsResult, workDir string, rescan bool) string {
	if len(res.Items) == 0 {
		if !rescan {
			// Never let this read as "the project is fine": without a rescan
			// it only reflects open buffers plus a possibly-stale scan.
			return "no diagnostics currently known for the workspace. Note this covers files " +
				"open in the editor plus whatever the last whole-project scan found — it is not " +
				"a build, and edits to files that aren't open may not be reflected. Re-run with " +
				"rescan=true for an up-to-date answer."
		}
		return fmt.Sprintf("no errors or warnings found across the workspace (scanned, waited %s). "+
			"Language-server coverage still only extends to files open in the editor, so this is "+
			"strong evidence but not a substitute for a build.", workspaceRescanWait)
	}

	// Report errors and warnings only. The question this tool answers is
	// "what is broken", and a real workspace is dominated by info-level
	// output — on this repo a scan returns ~500 spell-check hints against
	// prose files, which both buries the compile errors and hits the server's
	// result cap. The suppressed count is still reported so nothing looks
	// hidden.
	var items []client.ClientWorkspaceDiag
	suppressed := 0
	for _, d := range res.Items {
		if d.Severity == 1 || d.Severity == 2 {
			items = append(items, d)
		} else {
			suppressed++
		}
	}
	if len(items) == 0 {
		return fmt.Sprintf("no errors or warnings across the workspace (%d info/hint-level "+
			"diagnostics suppressed). Language-server coverage extends only to files open in "+
			"the editor, so this is evidence, not a build.", suppressed)
	}

	// Group by file so the output reads like a build log rather than a flat list.
	byFile := map[string][]client.ClientWorkspaceDiag{}
	var order []string
	for _, d := range items {
		if _, seen := byFile[d.Path]; !seen {
			order = append(order, d.Path)
		}
		byFile[d.Path] = append(byFile[d.Path], d)
	}
	sort.Strings(order)

	var b strings.Builder
	fmt.Fprintf(&b, "%d error(s)/warning(s) across %d file(s)", len(items), len(order))
	if suppressed > 0 {
		fmt.Fprintf(&b, " (%d info/hint-level diagnostics not shown)", suppressed)
	}
	b.WriteString(":\n")
	for _, path := range order {
		rel := path
		if r, err := filepath.Rel(workDir, path); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
		for _, d := range byFile[path] {
			src := d.Source
			if src == "" {
				src = "lsp"
			}
			fmt.Fprintf(&b, "  %s:%d:%d %s: %s [%s]\n",
				rel, d.Line+1, d.Col+1, severityLabel(d.Severity), d.Message, src)
		}
	}
	if res.Truncated {
		b.WriteString("  (list truncated by the server — more diagnostics exist)\n")
	}
	return b.String()
}

// workspaceHasErrorOrWarning is workspaceHasErrorOrWarning's counterpart for
// the workspace diagnostic type, which carries a path the per-file one does
// not.
func workspaceHasErrorOrWarning(items []client.ClientWorkspaceDiag) bool {
	for _, d := range items {
		if d.Severity == 1 || d.Severity == 2 {
			return true
		}
	}
	return false
}

// ─── entry points that take a bare symbol name ────────────────────────────────
//
// Every navigation tool here originally required a path, which quietly made
// the whole set unreachable: asked "what calls X?" with no prior context, an
// agent has no path, and the cheapest way to get one is to grep for the name —
// at which point the question is already answered and the language server
// never gets a turn. Following the tools was strictly more work than ignoring
// them, so they were ignored.
//
// These two helpers remove that: a symbol name alone is now enough to start.

// lspExtensions returns the file extensions that have a language server
// configured, so a representative file can be chosen from among the languages
// that can actually answer a query.
func lspExtensions() map[string]bool {
	out := map[string]bool{}
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return out
	}
	for _, ls := range cfg.EffectiveLanguageServers() {
		for _, ext := range ls.Extensions {
			out[strings.TrimPrefix(ext, ".")] = true
		}
	}
	return out
}

// representativeFiles picks files whose language has a server configured,
// preferring whichever such language the project has most of, and spread
// across distinct top-level directories.
//
// Several rather than one, because a monorepo has several TypeScript/Go
// *projects*, each with its own language-server scope. Picking a single
// arbitrary file lands in one of them, and a workspace-wide symbol search from
// there sees only that project — in harmony, the first .ts file is
// packages/util/env.ts, whose server knows nothing about services/harmony, so
// every query returned "not found" for symbols that plainly exist.
//
// Uses `git ls-files` where possible: fast, and it respects .gitignore, so
// build output and vendored dependencies do not skew the choice.
func representativeFiles(workDir string) []string {
	exts := lspExtensions()
	if len(exts) == 0 {
		return nil
	}

	var names []string
	if isGitRepo(workDir) {
		out, err := exec.Command("git", "-C", workDir, "ls-files").Output()
		if err != nil {
			return nil
		}
		names = strings.Split(string(out), "\n")
	} else {
		// Bounded walk; a workspace that isn't a repo is usually small, and an
		// unbounded walk on a huge tree would be worse than not answering.
		const maxWalk = 5000
		seen := 0
		_ = filepath.WalkDir(workDir, func(pth string, d os.DirEntry, err error) error {
			if err != nil || seen > maxWalk {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if n := d.Name(); n == "node_modules" || (n != "." && strings.HasPrefix(n, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			seen++
			if rel, err := filepath.Rel(workDir, pth); err == nil {
				names = append(names, rel)
			}
			return nil
		})
	}

	counts := map[string]int{}
	for _, n := range names {
		ext := strings.TrimPrefix(filepath.Ext(strings.TrimSpace(n)), ".")
		if exts[ext] {
			counts[ext]++
		}
	}
	bestExt, bestN := "", 0
	for ext, n := range counts {
		// Tie-break on name so the choice is stable run to run rather than
		// depending on map iteration order.
		if n > bestN || (n == bestN && ext < bestExt) {
			bestExt, bestN = ext, n
		}
	}
	if bestExt == "" {
		return nil
	}

	// One candidate per top-level directory, so the set spans a monorepo's
	// separate projects instead of clustering in whichever happens to sort
	// first.
	const maxCandidates = 6
	var out []string
	seenTop := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || strings.TrimPrefix(filepath.Ext(n), ".") != bestExt {
			continue
		}
		top := n
		if i := strings.IndexByte(n, filepath.Separator); i > 0 {
			top = n[:i]
		}
		if seenTop[top] {
			continue
		}
		seenTop[top] = true
		out = append(out, n)
		if len(out) >= maxCandidates {
			break
		}
	}
	return out
}

// symbolResolveWait bounds how long resolveSymbol waits for a language server
// to finish indexing before concluding a symbol does not exist. Generous
// because a cold tsserver on a large TypeScript project is slow to become
// useful, and a wrong "not found" sends the caller straight back to grep.
const symbolResolveWait = 20 * time.Second

// resolveSymbol finds where a named symbol is defined, so a caller who knows
// only a name can reach the position-based lookups.
//
// Prefers an exact name match over the fuzzy matches a language server's
// workspace search also returns, and reports when several things share the
// name so the caller can disambiguate with an explicit path rather than
// silently getting the wrong one.
func resolveSymbol(ctx context.Context, rpc *client.RPC, workDir, symbol, hintPath string) (sym client.ClientSymbol, others []client.ClientSymbol, err error) {
	candidates := []string{hintPath}
	if hintPath == "" {
		candidates = representativeFiles(workDir)
		if len(candidates) == 0 {
			return sym, nil, fmt.Errorf("no file with a configured language server found in this "+
				"workspace, so there is nothing to resolve %q against; pass path explicitly", symbol)
		}
	}

	// Budget split across candidates: a cold language server needs time, but
	// so does discovering that this candidate's project simply doesn't contain
	// the symbol.
	per := symbolResolveWait / time.Duration(len(candidates))

	var syms []client.ClientSymbol
	for _, cand := range candidates {
		bufID, _, done, err := openForRead(ctx, rpc, workDir, cand)
		if err != nil {
			continue // an unreadable candidate is not worth failing over
		}

		// Retry while the language server warms up. A workspace symbol query
		// against a server that has only just started returns an empty list,
		// not an error — indistinguishable from "no such symbol".
		deadline := time.Now().Add(per)
		for {
			got, err := rpc.WorkspaceSymbols(ctx, bufID, symbol)
			if err != nil {
				done()
				return sym, nil, fmt.Errorf("symbol lookup failed: %w", err)
			}
			if len(got) > 0 {
				syms = got
				break
			}
			if time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				done()
				return sym, nil, ctx.Err()
			case <-time.After(diagnosticsPoll):
			}
		}
		done()
		if len(syms) > 0 {
			break
		}
	}

	var exact []client.ClientSymbol
	for _, s := range syms {
		if s.Name == symbol {
			exact = append(exact, s)
		}
	}
	if len(exact) == 0 {
		if len(syms) == 0 {
			return sym, nil, fmt.Errorf("no symbol named %q found in this workspace", symbol)
		}
		// Only fuzzy matches: better to name them than to guess.
		return sym, syms, fmt.Errorf("no symbol is named exactly %q", symbol)
	}
	return exact[0], exact[1:], nil
}
