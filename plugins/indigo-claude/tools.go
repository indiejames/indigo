package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
)

func allTools() []toolDef {
	return []toolDef{
		{
			Name:        "read_file",
			Description: "Read the complete contents of a file. Returns buffer content (including unsaved edits) when the file is open in the editor.",
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"path": {Type: "string", Description: "Path to the file, relative to workspace root or absolute."},
				},
				Required: []string{"path"},
			},
		},
		{
			Name:        "list_files",
			Description: "List files and directories inside a directory.",
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"path":      {Type: "string", Description: "Directory path, relative to workspace root. Defaults to workspace root if empty."},
					"recursive": {Type: "string", Description: "Set to 'true' to list recursively (depth-limited). Default is shallow."},
				},
			},
		},
		{
			Name:        "search_files",
			Description: "Search for a pattern across files in the workspace using grep. Returns matching lines with file paths and line numbers.",
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
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
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
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
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"path":   {Type: "string", Description: "File to edit, relative to workspace root or absolute."},
					"reason": {Type: "string", Description: "Short explanation of why this edit is needed."},
					"line":   {Type: "integer", Description: "1-based line number the inserted text should end up on."},
					"text":   {Type: "string", Description: "The line(s) to insert."},
				},
				Required: []string{"path", "reason", "line", "text"},
			},
		},
		{
			Name:        "save_file",
			Description: "Write a file's live editor buffer to disk. Approved edits apply to the buffer immediately but the on-disk file stays stale until saved — call this on every file you edited before running disk-based commands (builds, tests, grep). If the file is open in the editor the user is asked to approve the save (it may include their own unsaved changes).",
			InputSchema: toolSchema{
				Type: "object",
				Properties: map[string]schemaProp{
					"path": {Type: "string", Description: "File to save, relative to workspace root or absolute."},
				},
				Required: []string{"path"},
			},
		},
	}
}

// ─── input types ─────────────────────────────────────────────────────────────

type readFileInput struct {
	Path string `json:"path"`
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

// editSpec is one old→new replacement shown in the permission prompt.
type editSpec struct {
	path    string
	oldText string
	newText string
}

// ─── dispatcher ───────────────────────────────────────────────────────────────

func execTool(ctx context.Context, rpc *client.RPC, prog *programLink, workDir, name string, rawInput json.RawMessage) (string, bool) {
	switch name {
	case "read_file":
		var in readFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execReadFile(ctx, rpc, workDir, in.Path)
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
		return execApplyEdits(ctx, rpc, prog, workDir, in)
	case "insert_at_line":
		var in insertAtLineInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execInsertAtLine(ctx, rpc, prog, workDir, in)
	case "save_file":
		var in readFileInput
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return fmt.Sprintf("bad input: %v", err), true
		}
		return execSaveFile(ctx, rpc, prog, workDir, in.Path)
	default:
		return fmt.Sprintf("unknown tool: %s", name), true
	}
}

// ─── read_file ────────────────────────────────────────────────────────────────

func execReadFile(ctx context.Context, rpc *client.RPC, workDir, path string) (string, bool) {
	abs := absPath(workDir, path)
	bufID, content, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		data, ferr := os.ReadFile(abs)
		if ferr != nil {
			return fmt.Sprintf("cannot read %s: %v", path, ferr), true
		}
		return string(data), false
	}
	count, cerr := rpc.BufferClientCount(ctx, bufID)
	if cerr == nil && count == 1 {
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
	}
	return content, false
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
		args := []string{"-C", workDir, "grep", "-n", pattern}
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

// ─── apply_edits ──────────────────────────────────────────────────────────────

func execApplyEdits(ctx context.Context, rpc *client.RPC, prog *programLink, workDir string, in applyEditsInput) (string, bool) {
	abs := absPath(workDir, in.Path)

	// Block the agent goroutine until the user approves or rejects.
	replyCh := make(chan bool, 1)
	prog.emit(permissionRequestMsg{
		file:    in.Path,
		reason:  in.Reason,
		edits:   []editSpec{{path: abs, oldText: in.OldText, newText: in.NewText}},
		replyCh: replyCh,
	})
	if !<-replyCh {
		return "edit rejected by user", true
	}

	// Open the buffer (idempotent if already open).
	bufID, content, version, _, err := rpc.OpenFile(ctx, abs)
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
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		return fmt.Sprintf("edited and saved %s", in.Path), false
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

func execInsertAtLine(ctx context.Context, rpc *client.RPC, prog *programLink, workDir string, in insertAtLineInput) (string, bool) {
	abs := absPath(workDir, in.Path)

	// Block the agent goroutine until the user approves or rejects.
	replyCh := make(chan bool, 1)
	prog.emit(permissionRequestMsg{
		file:    fmt.Sprintf("%s (insert at line %d)", in.Path, in.Line),
		reason:  in.Reason,
		edits:   []editSpec{{path: abs, oldText: "", newText: in.Text}},
		replyCh: replyCh,
	})
	if !<-replyCh {
		return "edit rejected by user", true
	}

	bufID, content, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		return fmt.Sprintf("cannot open %s: %v", in.Path, err), true
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	weOpened := count == 1

	if _, err := rpc.ApplyOp(ctx, bufID, insertLineOp(content, in.Text, in.Line)); err != nil {
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
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		return fmt.Sprintf("inserted at line %d and saved %s", in.Line, in.Path), false
	}
	return fmt.Sprintf("inserted at line %d in %s — applied to the live buffer (approved by the user). Not yet on disk: call save_file before disk-based builds/tests.", in.Line, in.Path), false
}

// ─── save_file ────────────────────────────────────────────────────────────────

// execSaveFile writes the live buffer for path to disk so disk-based tools
// (builds, tests, grep) see edits that were applied to the buffer. When the
// buffer is open in an editor it may hold the user's own unsaved changes, so
// the save must be approved; when only the agent has it open, every unsaved
// byte was already approved edit by edit and the save is silent.
func execSaveFile(ctx context.Context, rpc *client.RPC, prog *programLink, workDir, path string) (string, bool) {
	abs := absPath(workDir, path)
	bufID, _, _, _, err := rpc.OpenFile(ctx, abs)
	if err != nil {
		return fmt.Sprintf("cannot open %s: %v", path, err), true
	}
	count, _ := rpc.BufferClientCount(ctx, bufID)
	weOpened := count == 1

	if !weOpened {
		replyCh := make(chan bool, 1)
		prog.emit(permissionRequestMsg{
			file:    path,
			reason:  "Save buffer to disk so builds/tests see the edits. Any of your own unsaved changes in this file will be saved too.",
			replyCh: replyCh,
		})
		if !<-replyCh {
			return "save rejected by user — on-disk file unchanged; buffer edits remain applied in the editor", true
		}
	}

	if err := rpc.Save(ctx, bufID); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("save failed for %s: %v", path, err), true
	}
	if weOpened {
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
	}
	return fmt.Sprintf("saved %s to disk", path), false
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func absPath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

func offsetToLineCol(s string, offset int) (line, col int) {
	for i := 0; i < offset && i < len(s); i++ {
		if s[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}
