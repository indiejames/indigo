package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/indiejames/indigo/internal/procutil"
)

// Client manages a single language server, either a spawned process (cmd
// set) or a TCP connection to one already running elsewhere (netConn set —
// see NewTCPClient). Exactly one of the two is non-nil for a given Client.
type Client struct {
	cmd     *exec.Cmd
	netConn net.Conn
	conn    *jsonrpcConn
	logFile *os.File

	mu          sync.Mutex
	docVersions map[string]int // uri → version
	initialized bool
	caps        ServerCapabilities
	rootURI     string

	diagMu      sync.RWMutex
	diagnostics map[string][]Diagnostic // uri → diags

	closed atomic.Bool // set once the connection's read loop exits (process died)
}

// Alive reports whether the language server's connection is still up. It
// goes false once the process crashes or its pipe closes; callers (namely
// Manager) should stop using a Client once this is false and start a fresh
// one instead of letting every call against it hang until its own timeout.
func (c *Client) Alive() bool { return !c.closed.Load() }

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
			logFile.Close()                                                                             //nolint:errcheck
		}
		return nil, fmt.Errorf("language server %q not found: %w", command, err)
	}

	cmd := exec.Command(resolved, args...)
	procutil.SetPgid(cmd)
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
	c.conn = newJSONRPCConnWithClose(stdout, stdin, c.handleNotification, nil, func() { c.closed.Store(true) })
	return c, nil
}

// tcpDialTimeout bounds how long NewTCPClient waits to connect. Short,
// unlike a process spawn: an unreachable address (Godot not running, wrong
// port) should fail fast so Manager.startClient's caller isn't blocked and
// the failedStarts cooldown kicks in promptly.
const tcpDialTimeout = 3 * time.Second

// NewTCPClient connects to a language server already listening on address
// instead of spawning one — the shape Godot's GDScript language server
// uses (it only exists as a TCP listener, default "localhost:6005", inside
// an already-running Godot editor process with the project open; indigo
// has no way to launch Godot itself). Initialize() must still be called
// before using the client, exactly as with NewClient. jsonrpcConn only
// needs an io.Reader/io.Writer pair (proven by the net.Pipe()-backed test
// in crash_recovery_test.go), so a net.Conn wires in the same way NewClient
// wires stdout/stdin.
func NewTCPClient(address, rootDir string) (*Client, error) {
	conn, err := net.DialTimeout("tcp", address, tcpDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial language server at %q: %w", address, err)
	}

	c := &Client{
		netConn:     conn,
		docVersions: make(map[string]int),
		diagnostics: make(map[string][]Diagnostic),
		rootURI:     pathToURI(rootDir),
	}
	c.conn = newJSONRPCConnWithClose(conn, conn, c.handleNotification, nil, func() { c.closed.Store(true) })
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
				PublishDiagnostics: &PublishDiagnosticsClientCapabilities{RelatedInformation: true, VersionSupport: true},
				SemanticTokens: &SemanticTokensClientCapabilities{
					Requests: SemanticTokensRequestClientCapabilities{Range: true},
					TokenTypes: []string{
						"namespace", "type", "class", "enum", "interface", "struct",
						"typeParameter", "parameter", "variable", "property", "enumMember",
						"event", "function", "method", "macro", "keyword", "modifier",
						"comment", "string", "number", "regexp", "operator", "decorator",
					},
					TokenModifiers: []string{
						"declaration", "definition", "readonly", "static", "deprecated",
						"abstract", "async", "modification", "documentation", "defaultLibrary",
					},
					Formats: []string{"relative"},
				},
			},
		},
		InitializationOptions: map[string]any{
			// Let typescript-language-server handle files that have no tsconfig.json.
			"tsserver": map[string]any{
				"useSyntaxServer": "never",
			},
			// Inlay hints: parameter-name hints only (e.g. `foo(count: 5)`),
			// the highest-value/lowest-noise category. Both gopls and
			// typescript-language-server default every inlay-hint category to
			// off, so without this indigo would never receive a single hint
			// regardless of Config.InlayHints. The broader categories
			// (variable/return types, composite literal fields, etc.) are
			// deliberately left at each server's own default (off) — enabling
			// them produces a type annotation on nearly every `:=` in
			// idiomatic Go, measured at 58 hints in one 300-line file, far too
			// noisy as a default.
			//
			// Read by typescript-language-server. "literals" limits hints to
			// literal arguments (numbers, booleans, strings, etc.) where the
			// parameter name adds real information — e.g. `f(true, 5)` becomes
			// `f(enabled: true, count: 5)` — and skips variable-argument
			// calls like `f(userID)`, where the variable's own name already
			// conveys the same thing "all" would additionally annotate.
			"preferences": map[string]any{
				"includeInlayParameterNameHints": "literals",
			},
			// Read by gopls; matches the same parameter-name-only scope as
			// the preference above.
			"hints": map[string]any{
				"parameterNames": true,
			},
			// gopls only advertises semanticTokensProvider (and so only
			// responds to textDocument/semanticTokens/range) when this is
			// explicitly enabled — confirmed against a real gopls: declaring
			// the client capability alone was not enough. Read by gopls;
			// typescript-language-server advertises semantic tokens by
			// default and needs no equivalent setting.
			"semanticTokens": true,
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

// codeActionRequest is the shared implementation behind CodeActions and
// OrganizeImports: send textDocument/codeAction with the given range and
// context, decode the response (nil on a null result, not an error), and
// resolve any actions the server returned without a populated edit.
func (c *Client) codeActionRequest(path string, rng Range, actionCtx CodeActionContext) ([]CodeAction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/codeAction", CodeActionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Range:        rng,
		Context:      actionCtx,
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

// CodeActions returns quick-fix and refactor actions for the given range.
// startLine/startCol and endLine/endCol may be equal (a plain cursor
// position) or span a selection, which lets the server offer range-only
// actions like Extract Function/Extract Variable alongside point-based
// quick-fixes. Only the file's diagnostics that actually overlap the
// requested range are included in the request context — CodeActionContext's
// diagnostics field is specified as "diagnostics ... overlapping the range
// provided", and passing the whole file's diagnostics regardless of range
// let a server return quick-fixes for a diagnostic anywhere in the file, not
// just near the cursor/selection (e.g. gopls offering a far-away
// "replace if/else with max" suggestion no matter where the request was
// actually made).
func (c *Client) CodeActions(path string, startLine, startCol, endLine, endCol int) ([]CodeAction, error) {
	rng := Range{
		Start: Position{Line: startLine, Character: startCol},
		End:   Position{Line: endLine, Character: endCol},
	}
	var relevant []Diagnostic
	for _, d := range c.GetDiagnostics(path) {
		if diagnosticOverlapsRange(d, rng) {
			relevant = append(relevant, d)
		}
	}
	return c.codeActionRequest(path, rng, CodeActionContext{Diagnostics: relevant})
}

// diagnosticOverlapsRange reports whether d's range overlaps rng. rng.Start
// == rng.End is treated as a plain cursor position rather than a real
// range: touching at a single point still counts, so a cursor sitting
// exactly at a diagnostic's start/end column matches, and so does a
// genuinely zero-width diagnostic located exactly there. A non-zero-width
// rng (an active selection) instead uses strict half-open overlap —
// touching at a shared boundary with no actually-shared text is not a real
// overlap, e.g. a diagnostic that starts exactly where the selection ends.
func diagnosticOverlapsRange(d Diagnostic, rng Range) bool {
	if rng.Start == rng.End {
		return !posBefore(rng.Start, d.Range.Start) && !posBefore(d.Range.End, rng.Start)
	}
	return posBefore(d.Range.Start, rng.End) && posBefore(rng.Start, d.Range.End)
}

func posBefore(a, b Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

// OrganizeImports requests "source.organizeImports" code actions for path
// over its full range (0,0)-(lineCount,0). Unlike CodeActions this sends no
// diagnostics and instead restricts the response via Only, since organize-
// imports is a source action most servers only return when explicitly
// requested that way, not incidentally alongside diagnostic-driven fixes.
func (c *Client) OrganizeImports(path string, lineCount int) ([]CodeAction, error) {
	rng := Range{
		Start: Position{Line: 0, Character: 0},
		End:   Position{Line: lineCount, Character: 0},
	}
	return c.codeActionRequest(path, rng, CodeActionContext{Only: []string{"source.organizeImports"}})
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

// InlayHints returns inlay hints (inferred types, parameter names) for the
// given range. startLine/startCol and endLine/endCol are normally the client's
// visible viewport, not the whole file — servers can be slow on large files,
// and hints outside the viewport aren't rendered anyway.
func (c *Client) InlayHints(path string, startLine, startCol, endLine, endCol int) ([]InlayHint, error) {
	c.mu.Lock()
	supported := c.caps.InlayHintProvider != nil
	c.mu.Unlock()
	if !supported {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/inlayHint", InlayHintParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Range: Range{
			Start: Position{Line: startLine, Character: startCol},
			End:   Position{Line: endLine, Character: endCol},
		},
	})
	if err != nil || string(raw) == "null" {
		return nil, err
	}
	var hints []InlayHint
	if err := json.Unmarshal(raw, &hints); err != nil {
		return nil, err
	}
	return hints, nil
}

// SemanticTokensRange returns decoded semantic tokens for the given range —
// normally the client's visible viewport, not the whole file, both because
// servers can be slow on large files and because indigo only ever needs to
// color what's on screen. Returns (nil, nil) when the server doesn't support
// semantic tokens at all; this is checked upfront (unlike most methods on
// Client) because, like InlayHints, this will be polled periodically rather
// than called once per user action — skipping unsupported servers avoids
// wasting a request indefinitely.
func (c *Client) SemanticTokensRange(path string, startLine, startCol, endLine, endCol int) ([]SemanticToken, error) {
	c.mu.Lock()
	provider := c.caps.SemanticTokensProvider
	c.mu.Unlock()
	if provider == nil {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.conn.Call(ctx, "textDocument/semanticTokens/range", SemanticTokensParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Range: Range{
			Start: Position{Line: startLine, Character: startCol},
			End:   Position{Line: endLine, Character: endCol},
		},
	})
	if err != nil || string(raw) == "null" {
		return nil, err
	}
	var result SemanticTokensResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return decodeSemanticTokens(result.Data, provider.Legend), nil
}

// decodeSemanticTokens expands the delta-encoded Data array — 5 uint32s per
// token: [deltaLine, deltaStartChar, length, tokenType, tokenModifiers], per
// the LSP spec's SemanticTokens encoding — into absolute-position tokens,
// resolving the type/modifier indices via legend (which is per-server: never
// assume a fixed index means the same thing across servers). Per spec,
// deltaStartChar is relative to the previous token's start ONLY when
// deltaLine is 0 (same line); otherwise it's the new line's absolute start
// column. Malformed trailing data (not a multiple of 5) is ignored rather
// than panicking.
func decodeSemanticTokens(data []uint32, legend SemanticTokensLegend) []SemanticToken {
	var tokens []SemanticToken
	line, startChar := 0, 0
	for i := 0; i+5 <= len(data); i += 5 {
		deltaLine := int(data[i])
		deltaStartChar := int(data[i+1])
		length := int(data[i+2])
		typeIdx := int(data[i+3])
		modBits := data[i+4]

		if deltaLine == 0 {
			startChar += deltaStartChar
		} else {
			line += deltaLine
			startChar = deltaStartChar
		}

		var tokenType string
		if typeIdx >= 0 && typeIdx < len(legend.TokenTypes) {
			tokenType = legend.TokenTypes[typeIdx]
		}
		var mods []string
		for b, name := range legend.TokenModifiers {
			if modBits&(1<<uint(b)) != 0 {
				mods = append(mods, name)
			}
		}

		tokens = append(tokens, SemanticToken{
			Line: line, StartChar: startChar, Length: length,
			TokenType: tokenType, Modifiers: mods,
		})
	}
	return tokens
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
func (c *Client) Format(path, content string, opts FormattingOptions) (string, bool, error) {
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
		Options:      opts,
	}
	raw, err := c.conn.Call(ctx, "textDocument/formatting", params)
	if err != nil {
		var rpcErr *jsonrpcError
		if errors.As(err, &rpcErr) && rpcErr.Code == jsonrpcMethodNotFound {
			// Server advertised documentFormattingProvider but doesn't
			// actually implement the request — treat like "no formatting
			// support" (see jsonrpcMethodNotFound's doc comment) instead
			// of surfacing a hard error on every save.
			return content, false, nil
		}
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
	c.terminate()
}

// terminate reaps the underlying OS process and releases its resources
// (log file) without attempting the graceful LSP shutdown handshake —
// unlike Shutdown's request/notify pair, this is safe to call on a
// connection that's already dead (e.g. after a crash, where there's no
// reader left to ever answer a "shutdown" request). Idempotent: safe to
// call on an already-exited process.
func (c *Client) terminate() {
	if c.cmd != nil && c.cmd.Process != nil {
		procutil.KillGroup(c.cmd) //nolint:errcheck
		c.cmd.Wait()              //nolint:errcheck
	}
	if c.netConn != nil {
		c.netConn.Close() //nolint:errcheck
	}
	if c.logFile != nil {
		c.logFile.Close() //nolint:errcheck
	}
}

func (c *Client) handleNotification(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		if p.Version != nil {
			c.mu.Lock()
			current := c.docVersions[p.URI]
			c.mu.Unlock()
			if *p.Version != current {
				// Stale (or anomalously ahead of what we've sent via
				// DidChange/DidOpen) — discard rather than risk showing
				// diagnostics computed against the wrong buffer state.
				return
			}
		}
		c.diagMu.Lock()
		c.diagnostics[p.URI] = p.Diagnostics
		c.diagMu.Unlock()
	}
}
