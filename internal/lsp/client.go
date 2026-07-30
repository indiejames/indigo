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
			fmt.Fprintf(logFile, "LookPath(%q) failed: %v\nPATH=%s\n", command, err, os.Getenv("PATH")) //nolint:errcheck
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
	c.conn = newJSONRPCConn(stdout, stdin, c.handleNotification, nil)
	return c, nil
}

// Initialize performs the LSP handshake.
func (c *Client) Initialize() error {
	params := InitializeParams{
		ProcessID:  os.Getpid(),
		ClientInfo: ClientInfo{Name: "indigo", Version: "0.1"},
		RootURI:    c.rootURI,
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				CodeAction: &CodeActionClientCapabilities{
					DataSupport:    true,
					ResolveSupport: &CodeActionResolveSupport{Properties: []string{"edit"}},
				},
				Completion: &CompletionClientCapabilities{
					CompletionItem: &CompletionItemClientCapabilities{
						SnippetSupport: false,
						ResolveSupport: &CompletionItemResolveSupport{
							Properties: []string{"additionalTextEdits", "detail", "documentation"},
						},
					},
				},
				PublishDiagnostics: &PublishDiagnosticsClientCapabilities{RelatedInformation: true},
			},
		},
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
		fmt.Fprintf(c.logFile, "initialize response: err=%v raw=%s\n", err, raw) //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var result InitializeResult
	if unmarshalErr := json.Unmarshal(raw, &result); unmarshalErr != nil {
		if c.logFile != nil {
			fmt.Fprintf(c.logFile, "unmarshal capabilities error: %v\n", unmarshalErr) //nolint:errcheck
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
//
// The version bump and the notification write happen under the same lock
// (not just the bump) so concurrent DidChange calls for this client — e.g.
// ApplyOp/ApplyOps each fire one in their own goroutine — can't reach the
// server out of version order. LSP servers key their document snapshot on
// this version; two calls racing past the old bump-then-unlock split could
// hand out version 5 and 6 but write 6 before 5, leaving the server's
// documented state one edit behind (and any request sent right after,
// like a rename, silently computed against stale content).
func (c *Client) DidChange(path, content string) error {
	uri := pathToURI(path)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.docVersions[uri]++
	ver := c.docVersions[uri]

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

// ResolveCompletion asks the server to fill in the lazily-computed fields of a
// completion item — most importantly AdditionalTextEdits, which is how servers
// like typescript-language-server deliver the `import { X } from "…"` line for
// an auto-imported symbol. The top-level textDocument/completion response omits
// those edits (computing them for every candidate would be far too expensive);
// they only appear once the client resolves the single item it is about to
// insert, sending the item's opaque Data back unchanged. Returns the item
// unchanged if the server doesn't support completionItem/resolve or has nothing
// to add.
func (c *Client) ResolveCompletion(item CompletionItem) (CompletionItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "completionItem/resolve", item)
	if err != nil || string(raw) == "null" {
		return item, err
	}
	var resolved CompletionItem
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return item, err
	}
	return resolved, nil
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

// WorkspaceSymbols queries the language server for symbols matching query.
func (c *Client) WorkspaceSymbols(query string) ([]SymbolInformation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "workspace/symbol", WorkspaceSymbolParams{Query: query})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var syms []SymbolInformation
	if err := json.Unmarshal(raw, &syms); err != nil {
		return nil, err
	}
	return syms, nil
}

// DocumentSymbols returns symbols in path, flattened to a list.
// The server may return []DocumentSymbol (hierarchical) or []SymbolInformation (flat).
func (c *Client) DocumentSymbols(path string) ([]SymbolInformation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/documentSymbol", DocumentSymbolParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	// Try flat SymbolInformation first.
	var flat []SymbolInformation
	if err := json.Unmarshal(raw, &flat); err == nil && len(flat) > 0 && flat[0].Location.URI != "" {
		return flat, nil
	}
	// Fall back to hierarchical DocumentSymbol.
	var hier []DocumentSymbol
	if err := json.Unmarshal(raw, &hier); err != nil {
		return nil, err
	}
	uri := pathToURI(path)
	var result []SymbolInformation
	var flatten func(syms []DocumentSymbol)
	flatten = func(syms []DocumentSymbol) {
		for _, s := range syms {
			result = append(result, SymbolInformation{
				Name: s.Name,
				Kind: s.Kind,
				Location: Location{
					URI:   uri,
					Range: s.SelectionRange,
				},
			})
			flatten(s.Children)
		}
	}
	flatten(hier)
	return result, nil
}

// References returns all reference locations for the symbol at (line, col) in path.
func (c *Client) References(path string, line, col int) ([]Location, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/references", ReferenceParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
		Context:      ReferenceContext{IncludeDeclaration: false},
	})
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var locs []Location
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, err
	}
	return locs, nil
}

// CodeActions returns quick-fix and refactor actions for the given range.
// startLine/startCol and endLine/endCol may be equal (a plain cursor
// position) or span a selection, which lets the server offer range-only
// actions like Extract Function/Extract Variable alongside point-based
// quick-fixes. The server's cached diagnostics for the file are included in
// the request context so that diagnostic-driven fixes (e.g. "remove unused
// import") are surfaced too.
func (c *Client) CodeActions(path string, startLine, startCol, endLine, endCol int) ([]CodeAction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rng := Range{
		Start: Position{Line: startLine, Character: startCol},
		End:   Position{Line: endLine, Character: endCol},
	}
	diags := c.GetDiagnostics(path)
	raw, err := c.conn.Call(ctx, "textDocument/codeAction", CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Range:        rng,
		Context:      CodeActionContext{Diagnostics: diags},
	})
	if err != nil || string(raw) == "null" {
		return nil, err
	}
	var actions []CodeAction
	if err := json.Unmarshal(raw, &actions); err != nil {
		return nil, err
	}
	c.resolveCodeActions(actions)
	return actions, nil
}

// resolveCodeActions fills in the Edit for any action that came back without
// one, via codeAction/resolve. Many servers (gopls included) return
// "expensive" actions like Extract Function/Extract Variable with no edit
// populated, computing it lazily only once the client asks to resolve that
// specific action — skipping this step means those actions look like they
// have nothing to apply and get silently dropped. Resolved concurrently
// since each is a separate round trip; servers that don't support resolve
// (or reject a given action) just leave it with no edit, same as before.
func (c *Client) resolveCodeActions(actions []CodeAction) {
	var wg sync.WaitGroup
	for i := range actions {
		if actions[i].Edit != nil {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			raw, err := c.conn.Call(ctx, "codeAction/resolve", actions[i])
			if err != nil || string(raw) == "null" {
				return
			}
			var resolved CodeAction
			if json.Unmarshal(raw, &resolved) == nil {
				actions[i] = resolved
			}
		}(i)
	}
	wg.Wait()
}

// Rename requests textDocument/rename for the symbol at (line, col) and
// returns the resulting WorkspaceEdit, or nil if the server doesn't support
// renaming, or found nothing to rename.
func (c *Client) Rename(path string, line, col int, newName string) (*WorkspaceEdit, error) {
	c.mu.Lock()
	supported := c.caps.RenameProvider != nil
	c.mu.Unlock()
	if !supported {
		return nil, nil
	}
	return c.rename(path, line, col, newName)
}

// RenameAfterChange notifies the server of content's current state and then
// immediately requests a rename at (line, col) — holding the client's lock
// across both the notification and the rename round trip, not just the
// notification's write.
//
// A rename issued right after an edit (e.g. Extract Function's automatic
// follow-up rename) needs the server's snapshot to reflect that edit before
// it computes the rename. DidChange alone isn't enough to guarantee that:
// nothing stops some other, unrelated DidChange call for this same client
// — most plausibly a slow-to-schedule goroutine from one of the edit's own
// ApplyOp calls — from landing in the gap between "my fresh notification"
// and "my rename request", invalidating the snapshot right as the rename
// arrives (observed as gopls returning "no identifier found" for a
// position that is in fact exactly on the target identifier). Holding the
// lock for the whole call, not just DidChange's write, closes that gap:
// every DidChange this client sends goes through the same lock, so none can
// interleave between the two calls made here.
func (c *Client) RenameAfterChange(path, content string, line, col int, newName string) (*WorkspaceEdit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	supported := c.caps.RenameProvider != nil
	if !supported {
		return nil, nil
	}

	uri := pathToURI(path)
	c.docVersions[uri]++
	ver := c.docVersions[uri]
	if err := c.conn.Notify("textDocument/didChange", DidChangeTextDocumentParams{
		TextDocument: VersionedTextDocumentIdentifier{URI: uri, Version: ver},
		ContentChanges: []TextDocumentContentChangeEvent{
			{Text: content},
		},
	}); err != nil {
		return nil, err
	}

	return c.rename(path, line, col, newName)
}

// rename performs the textDocument/rename round trip. Callers are
// responsible for checking RenameProvider support first.
func (c *Client) rename(path string, line, col int, newName string) (*WorkspaceEdit, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/rename", RenameParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     Position{Line: line, Character: col},
		NewName:      newName,
	})
	if err != nil || string(raw) == "null" {
		return nil, err
	}
	var edit WorkspaceEdit
	if err := json.Unmarshal(raw, &edit); err != nil {
		return nil, err
	}
	return &edit, nil
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
