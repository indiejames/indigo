package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Client manages a single language server process.
type Client struct {
	cmd     *exec.Cmd
	conn    *jsonrpcConn
	logFile *os.File

	mu          sync.Mutex
	docVersions map[string]int // uri → version
	initialized bool
	caps        ServerCapabilities
	rootURI     string

	diagMu      sync.RWMutex
	diagnostics map[string][]Diagnostic // uri → diags
}

// NewClient starts the language server process and returns a ready-to-use client.
// Initialize() must be called before using the client.
func NewClient(command string, args []string, rootDir string) (*Client, error) {
	// Open the log file first so we can record LookPath failures too.
	logPath := filepath.Join(os.TempDir(), "indigo-lsp-"+filepath.Base(command)+".log")
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)

	resolved, err := exec.LookPath(command)
	if err != nil {
		if logFile != nil {
			fmt.Fprintf(logFile, "LookPath(%q) failed: %v\nPATH=%s\n", command, err, os.Getenv("PATH"))
			logFile.Close() //nolint:errcheck
		}
		return nil, fmt.Errorf("language server %q not found: %w", command, err)
	}

	cmd := exec.Command(resolved, args...)
	if logFile != nil {
		cmd.Stderr = logFile
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", command, err)
	}

	c := &Client{
		cmd:         cmd,
		logFile:     logFile,
		docVersions: make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
		rootURI:     pathToURI(rootDir),
	}
	c.conn = newJSONRPCConn(stdout, stdin, c.handleNotification)
	return c, nil
}

// Initialize performs the LSP handshake.
func (c *Client) Initialize() error {
	params := InitializeParams{
		ProcessID:  os.Getpid(),
		ClientInfo: ClientInfo{Name: "indigo", Version: "0.1"},
		RootURI:    c.rootURI,
		InitializationOptions: map[string]any{
			// Let typescript-language-server handle files that have no tsconfig.json.
			"tsserver": map[string]any{
				"useSyntaxServer": "never",
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "initialize", params)
	if c.logFile != nil {
		fmt.Fprintf(c.logFile, "initialize response: err=%v raw=%s\n", err, raw)
	}
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var result InitializeResult
	if unmarshalErr := json.Unmarshal(raw, &result); unmarshalErr != nil {
		if c.logFile != nil {
			fmt.Fprintf(c.logFile, "unmarshal capabilities error: %v\n", unmarshalErr)
		}
		// Proceed with empty capabilities rather than aborting — the server is running.
	}
	c.mu.Lock()
	c.caps = result.Capabilities
	c.initialized = true
	c.mu.Unlock()

	return c.conn.Notify("initialized", struct{}{})
}

// DidOpen notifies the server that a file has been opened.
// Idempotent: if the file is already tracked, returns without sending a second notification.
func (c *Client) DidOpen(path, content string) error {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	uri := pathToURI(path)

	c.mu.Lock()
	if _, already := c.docVersions[uri]; already {
		c.mu.Unlock()
		return nil
	}
	c.docVersions[uri] = 1
	c.mu.Unlock()

	return c.conn.Notify("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: languageIDForExt(ext),
			Version:    1,
			Text:       content,
		},
	})
}

// DidChange notifies the server of a content change (full-text sync).
func (c *Client) DidChange(path, content string) error {
	uri := pathToURI(path)

	c.mu.Lock()
	c.docVersions[uri]++
	ver := c.docVersions[uri]
	c.mu.Unlock()

	return c.conn.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: uri, Version: ver},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: content},
		},
	})
}

// DidSave notifies the server that a file has been saved.
func (c *Client) DidSave(path string) error {
	return c.conn.Notify("textDocument/didSave", DidSaveTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
	})
}

// DidClose notifies the server that a file has been closed.
func (c *Client) DidClose(path string) error {
	uri := pathToURI(path)
	c.mu.Lock()
	delete(c.docVersions, uri)
	c.mu.Unlock()
	c.diagMu.Lock()
	delete(c.diagnostics, uri)
	c.diagMu.Unlock()
	return c.conn.Notify("textDocument/didClose", DidCloseTextDocumentParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
	})
}

// Hover returns hover information at the given position, or nil if none.
func (c *Client) Hover(path string, line, col int) (*Hover, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/hover", HoverParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var h Hover
	if err := json.Unmarshal(raw, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// SignatureHelp returns signature help at the given position, or nil if none.
func (c *Client) SignatureHelp(path string, line, col int) (*SignatureHelp, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/signatureHelp", SignatureHelpParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var sh SignatureHelp
	if err := json.Unmarshal(raw, &sh); err != nil {
		return nil, err
	}
	return &sh, nil
}

// Complete returns completion items at the given position.
func (c *Client) Complete(path string, line, col int) ([]CompletionItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}

	// The response may be CompletionList or []CompletionItem.
	var list CompletionList
	if err := json.Unmarshal(raw, &list); err == nil && list.Items != nil {
		return list.Items, nil
	}
	var items []CompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// Definition returns the definition location(s) for the symbol at (line, col).
func (c *Client) Definition(path string, line, col int) ([]Location, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/definition", DefinitionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	// Response may be Location or []Location.
	var single Location
	var multi []Location
	if err := json.Unmarshal(raw, &multi); err == nil && len(multi) > 0 {
		return multi, nil
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{single}, nil
	}
	return nil, nil
}

// GetDiagnostics returns the most recent diagnostics for path.
func (c *Client) GetDiagnostics(path string) []Diagnostic {
	c.diagMu.RLock()
	defer c.diagMu.RUnlock()
	diags := c.diagnostics[pathToURI(path)]
	out := make([]Diagnostic, len(diags))
	copy(out, diags)
	return out
}

// Format requests textDocument/formatting and applies the returned edits.
// Returns (content, false, nil) when the server reports no formatting support.
func (c *Client) Format(path, content string) (string, bool, error) {
	c.mu.Lock()
	hasFormatting := c.caps.DocumentFormattingProvider != nil
	c.mu.Unlock()
	if !hasFormatting {
		return content, false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := DocumentFormattingParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Options:      FormattingOptions{TabSize: 4, InsertSpaces: true},
	}
	raw, err := c.conn.Call(ctx, "textDocument/formatting", params)
	if err != nil {
		return content, false, err
	}
	if string(raw) == "null" {
		return content, false, nil
	}

	var edits []TextEdit
	if err := json.Unmarshal(raw, &edits); err != nil {
		return content, false, err
	}
	if len(edits) == 0 {
		return content, false, nil
	}

	result, err := applyEdits(content, edits)
	if err != nil {
		return content, false, err
	}
	return result, result != content, nil
}

// applyEdits applies a set of LSP TextEdits to content, returning the result.
// Edits are applied from last to first to preserve earlier positions.
func applyEdits(content string, edits []TextEdit) (string, error) {
	lines := strings.Split(content, "\n")

	sort.Slice(edits, func(i, j int) bool {
		a, b := edits[i].Range.Start, edits[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})

	for _, edit := range edits {
		s, e := edit.Range.Start, edit.Range.End
		if s.Line >= len(lines) || e.Line >= len(lines) {
			return content, fmt.Errorf("edit range out of bounds")
		}
		startRunes := []rune(lines[s.Line])
		endRunes := []rune(lines[e.Line])

		sc := s.Character
		if sc > len(startRunes) {
			sc = len(startRunes)
		}
		ec := e.Character
		if ec > len(endRunes) {
			ec = len(endRunes)
		}

		prefix := string(startRunes[:sc])
		suffix := string(endRunes[ec:])
		replacement := strings.Split(prefix+edit.NewText+suffix, "\n")

		updated := make([]string, 0, len(lines)-(e.Line-s.Line+1)+len(replacement))
		updated = append(updated, lines[:s.Line]...)
		updated = append(updated, replacement...)
		updated = append(updated, lines[e.Line+1:]...)
		lines = updated
	}

	return strings.Join(lines, "\n"), nil
}

// Shutdown sends shutdown + exit and kills the process.
func (c *Client) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.conn.Call(ctx, "shutdown", nil) //nolint:errcheck
	c.conn.Notify("exit", nil)        //nolint:errcheck
	c.cmd.Process.Kill() //nolint:errcheck
	c.cmd.Wait() //nolint:errcheck
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		c.diagMu.Lock()
		c.diagnostics[p.URI] = p.Diagnostics
		c.diagMu.Unlock()
	}
}
