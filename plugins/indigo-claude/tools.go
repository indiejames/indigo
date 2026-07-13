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

	if _, err := rpc.ApplyOp(ctx, bufID, document.Op{
		Type:     document.OpDelete,
		FromLine: startLine,
		FromCol:  startCol,
		ToLine:   endLine,
		ToCol:    endCol,
		Version:  version,
	}); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("delete op failed: %v", err), true
	}

	if _, err := rpc.ApplyOp(ctx, bufID, document.Op{
		Type:       document.OpInsert,
		InsertLine: startLine,
		InsertCol:  startCol,
		InsertText: in.NewText,
	}); err != nil {
		if weOpened {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		}
		return fmt.Sprintf("insert op failed: %v", err), true
	}

	if weOpened {
		if serr := rpc.Save(ctx, bufID); serr != nil {
			rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
			return fmt.Sprintf("edited %s (save failed: %v)", in.Path, serr), false
		}
		rpc.CloseBuffer(ctx, bufID) //nolint:errcheck
		return fmt.Sprintf("edited and saved %s", in.Path), false
	}
	return fmt.Sprintf("edited %s (open in editor — review and save when ready)", in.Path), false
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
