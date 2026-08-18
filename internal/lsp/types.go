package lsp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// pathToURI converts an absolute file path to a file URI.
func pathToURI(absPath string) string { return "file://" + absPath }

// URIToPath strips the file:// prefix.
func URIToPath(uri string) string {
	if len(uri) > 7 && uri[:7] == "file://" {
		return uri[7:]
	}
	return uri
}

// languageIDForExt maps a file extension to an LSP language identifier.
func languageIDForExt(ext string) string {
	m := map[string]string{
		"go":   "go",
		"rs":   "rust",
		"ts":   "typescript",
		"tsx":  "typescriptreact",
		"js":   "javascript",
		"jsx":  "javascriptreact",
		"py":   "python",
		"c":    "c",
		"cpp":  "cpp",
		"h":    "c",
		"hpp":  "cpp",
		"java": "java",
		"cs":   "csharp",
		"rb":   "ruby",
		"lua":  "lua",
		"zig":  "zig",
	}
	if id, ok := m[ext]; ok {
		return id
	}
	return ext
}

// ---- LSP core types ----

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// ---- Initialize ----

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type TextDocumentSyncKind int

const (
	SyncNone        TextDocumentSyncKind = 0
	SyncFull        TextDocumentSyncKind = 1
	SyncIncremental TextDocumentSyncKind = 2
)

type ServerCapabilities struct {
	// TextDocumentSync is json.RawMessage because the LSP spec allows either
	// a TextDocumentSyncKind integer or a TextDocumentSyncOptions object.
	TextDocumentSync           json.RawMessage        `json:"textDocumentSync,omitempty"`
	HoverProvider              any                    `json:"hoverProvider,omitempty"`
	SignatureHelpProvider      any                    `json:"signatureHelpProvider,omitempty"`
	CompletionProvider         any                    `json:"completionProvider,omitempty"`
	DocumentFormattingProvider any                    `json:"documentFormattingProvider,omitempty"`
	RenameProvider             any                    `json:"renameProvider,omitempty"`
	InlayHintProvider          any                    `json:"inlayHintProvider,omitempty"`
	SemanticTokensProvider     *SemanticTokensOptions `json:"semanticTokensProvider,omitempty"`
}

// SemanticTokensLegend maps the token type/modifier indices used in a
// SemanticTokens.Data array to their string names. Negotiated once per
// server at initialize — different servers define different legends (e.g.
// gopls and typescript-language-server use different orderings and even
// different type vocabularies), so an index must never be interpreted
// without the legend that produced it.
type SemanticTokensLegend struct {
	TokenTypes     []string `json:"tokenTypes"`
	TokenModifiers []string `json:"tokenModifiers"`
}

// SemanticTokensOptions is the shape of ServerCapabilities.SemanticTokensProvider.
// Range/Full are json.RawMessage because the spec allows either a bool or an
// options object for each; indigo only needs to know the legend and whether
// range requests are supported at all (presence, not the object's contents).
type SemanticTokensOptions struct {
	Legend SemanticTokensLegend `json:"legend"`
	Range  json.RawMessage      `json:"range,omitempty"`
	Full   json.RawMessage      `json:"full,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

// CodeActionResolveSupport advertises which CodeAction properties the client
// can obtain lazily via codeAction/resolve.
type CodeActionResolveSupport struct {
	Properties []string `json:"properties"`
}

// CodeActionClientCapabilities advertises code-action support. Declaring
// dataSupport + resolveSupport(edit) matters: without them servers like
// gopls return range refactors (Extract Function/Extract Variable) as
// opaque Command-style actions whose edits can only be obtained through a
// workspace/executeCommand + workspace/applyEdit round trip; with them the
// server returns resolvable actions whose edit codeAction/resolve fills in.
type CodeActionClientCapabilities struct {
	DataSupport    bool                      `json:"dataSupport,omitempty"`
	ResolveSupport *CodeActionResolveSupport `json:"resolveSupport,omitempty"`
}

// PublishDiagnosticsClientCapabilities advertises that the client wants
// textDocument/publishDiagnostics notifications. This isn't optional in
// practice: gopls pushes diagnostics regardless of whether the client
// declares this, but typescript-language-server checks for it and silently
// never sends a single publishDiagnostics notification without it — no
// error, just permanent silence for that document.
type PublishDiagnosticsClientCapabilities struct {
	RelatedInformation bool `json:"relatedInformation,omitempty"`
	// VersionSupport tells the server we understand and use the
	// notification's version field — without declaring this, a
	// spec-conformant server may omit it, defeating handleNotification's
	// staleness check.
	VersionSupport bool `json:"versionSupport,omitempty"`
}

// CompletionItemResolveSupport advertises which CompletionItem properties the
// client can obtain lazily via completionItem/resolve.
type CompletionItemResolveSupport struct {
	Properties []string `json:"properties"`
}

// CompletionItemClientCapabilities advertises per-item completion support.
// Declaring resolveSupport with "additionalTextEdits" is what lets a server
// defer the auto-import edit to completionItem/resolve rather than computing
// it eagerly for every candidate (prohibitively expensive) or not at all;
// indigo resolves the single accepted item to fetch it. snippetSupport is
// false because the client inserts completion text literally and does not
// interpret ${1:…}-style snippet placeholders.
type CompletionItemClientCapabilities struct {
	SnippetSupport bool                          `json:"snippetSupport"`
	ResolveSupport *CompletionItemResolveSupport `json:"resolveSupport,omitempty"`
}

// CompletionClientCapabilities advertises textDocument/completion support.
type CompletionClientCapabilities struct {
	CompletionItem *CompletionItemClientCapabilities `json:"completionItem,omitempty"`
}

// SemanticTokensRequestClientCapabilities declares which semantic-tokens
// request shapes the client will use. indigo only ever requests ranges (the
// visible viewport), matching how inlay hints are fetched.
type SemanticTokensRequestClientCapabilities struct {
	Range bool `json:"range,omitempty"`
}

// SemanticTokensClientCapabilities advertises textDocument/semanticTokens
// support. TokenTypes/TokenModifiers list the standard vocabulary defined by
// the LSP spec — purely informational (servers report their own legend
// separately in SemanticTokensOptions, which indigo always consults instead
// of assuming any fixed index meaning); Formats must include "relative", the
// only encoding format the spec defines.
type SemanticTokensClientCapabilities struct {
	Requests       SemanticTokensRequestClientCapabilities `json:"requests"`
	TokenTypes     []string                                `json:"tokenTypes"`
	TokenModifiers []string                                `json:"tokenModifiers"`
	Formats        []string                                `json:"formats"`
}

type TextDocumentClientCapabilities struct {
	CodeAction         *CodeActionClientCapabilities         `json:"codeAction,omitempty"`
	Completion         *CompletionClientCapabilities         `json:"completion,omitempty"`
	PublishDiagnostics *PublishDiagnosticsClientCapabilities `json:"publishDiagnostics,omitempty"`
	SemanticTokens     *SemanticTokensClientCapabilities     `json:"semanticTokens,omitempty"`
}

type ClientCapabilities struct {
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
}

type InitializeParams struct {
	ProcessID             int                `json:"processId"`
	ClientInfo            ClientInfo         `json:"clientInfo"`
	RootURI               string             `json:"rootUri"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	InitializationOptions map[string]any     `json:"initializationOptions,omitempty"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// ---- textDocument/didOpen ----

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// ---- textDocument/didChange ----

type TextDocumentContentChangeEvent struct {
	Text string `json:"text"` // full-text sync: no range field
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// ---- textDocument/didSave ----

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// ---- textDocument/didClose ----

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// ---- textDocument/publishDiagnostics ----

type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI string `json:"uri"`
	// Version is the document version the server computed these diagnostics
	// against (optional per spec — omitted by some servers). A pointer so an
	// explicit 0 can be told apart from an omitted field; a notification
	// whose version doesn't match what we've since tracked via DidChange is
	// stale (or anomalously ahead) and must not overwrite current
	// diagnostics; see handleNotification.
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// ---- textDocument/formatting ----

type FormattingOptions struct {
	TabSize      int  `json:"tabSize"`
	InsertSpaces bool `json:"insertSpaces"`
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Options      FormattingOptions      `json:"options"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// ---- textDocument/hover ----

type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type MarkupContent struct {
	Kind  string `json:"kind"` // "plaintext" or "markdown"
	Value string `json:"value"`
}

type Hover struct {
	// Contents is json.RawMessage because the LSP spec allows three shapes:
	// MarkupContent {kind,value}, MarkedString {language,value}|string, or []MarkedString.
	Contents json.RawMessage `json:"contents"`
	Range    *Range          `json:"range,omitempty"`
}

func (h *Hover) Text() string {
	if h == nil || len(h.Contents) == 0 {
		return ""
	}
	// Try MarkupContent {kind, value}.
	var mc MarkupContent
	if err := json.Unmarshal(h.Contents, &mc); err == nil && mc.Value != "" {
		return mc.Value
	}
	// Try plain string (legacy MarkedString).
	var s string
	if err := json.Unmarshal(h.Contents, &s); err == nil {
		return s
	}
	// Try array of MarkedString ({language,value} | string).
	var arr []json.RawMessage
	if err := json.Unmarshal(h.Contents, &arr); err == nil {
		var parts []string
		for _, item := range arr {
			var ms struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			}
			if err := json.Unmarshal(item, &ms); err == nil && ms.Value != "" {
				parts = append(parts, ms.Value)
				continue
			}
			var plain string
			if err := json.Unmarshal(item, &plain); err == nil && plain != "" {
				parts = append(parts, plain)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// ---- textDocument/signatureHelp ----

type SignatureHelpParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type ParameterInformation struct {
	Label string `json:"label"`
}

type SignatureInformation struct {
	Label         string                 `json:"label"`
	Documentation string                 `json:"documentation,omitempty"`
	Parameters    []ParameterInformation `json:"parameters,omitempty"`
}

type SignatureHelp struct {
	Signatures      []SignatureInformation `json:"signatures"`
	ActiveSignature int                    `json:"activeSignature"`
	ActiveParameter int                    `json:"activeParameter"`
}

// ---- textDocument/completion ----

type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      *CompletionContext     `json:"context,omitempty"`
}

type CompletionItemKind int

const (
	KindText          CompletionItemKind = 1
	KindMethod        CompletionItemKind = 2
	KindFunction      CompletionItemKind = 3
	KindConstructor   CompletionItemKind = 4
	KindField         CompletionItemKind = 5
	KindVariable      CompletionItemKind = 6
	KindClass         CompletionItemKind = 7
	KindInterface     CompletionItemKind = 8
	KindModule        CompletionItemKind = 9
	KindProperty      CompletionItemKind = 10
	KindUnit          CompletionItemKind = 11
	KindValue         CompletionItemKind = 12
	KindEnum          CompletionItemKind = 13
	KindKeyword       CompletionItemKind = 14
	KindSnippet       CompletionItemKind = 15
	KindColor         CompletionItemKind = 16
	KindFile          CompletionItemKind = 17
	KindReference     CompletionItemKind = 18
	KindFolder        CompletionItemKind = 19
	KindEnumMember    CompletionItemKind = 20
	KindConstant      CompletionItemKind = 21
	KindStruct        CompletionItemKind = 22
	KindEvent         CompletionItemKind = 23
	KindOperator      CompletionItemKind = 24
	KindTypeParameter CompletionItemKind = 25
)

// KindAbbrev returns a short display label for a completion kind.
func KindAbbrev(k CompletionItemKind) string {
	switch k {
	case KindMethod:
		return "mth"
	case KindFunction:
		return "fn"
	case KindConstructor:
		return "new"
	case KindField:
		return "fld"
	case KindVariable:
		return "var"
	case KindClass:
		return "cls"
	case KindInterface:
		return "ifc"
	case KindModule:
		return "mod"
	case KindProperty:
		return "prp"
	case KindEnum:
		return "enm"
	case KindKeyword:
		return "kwd"
	case KindSnippet:
		return "snp"
	case KindConstant:
		return "cst"
	case KindStruct:
		return "str"
	case KindTypeParameter:
		return "typ"
	default:
		return fmt.Sprintf("%3d", int(k))
	}
}

type CompletionItem struct {
	Label      string             `json:"label"`
	Kind       CompletionItemKind `json:"kind,omitempty"`
	Detail     string             `json:"detail,omitempty"`
	InsertText string             `json:"insertText,omitempty"`

	// SortText and FilterText drive client-side ranking and filtering. Servers
	// deprioritize auto-import candidates via SortText (tsserver prefixes theirs
	// with U+FFFF so they sort last), and the client is expected to filter the
	// full list by the typed prefix — without that, auto-imports stay buried.
	// FilterText falls back to Label when empty.
	SortText   string `json:"sortText,omitempty"`
	FilterText string `json:"filterText,omitempty"`

	// TextEdit, when present, is the authoritative primary edit for accepting
	// this item (its range may differ from the typed prefix); prefer it over
	// InsertText/Label when applying.
	TextEdit *TextEdit `json:"textEdit,omitempty"`

	// AdditionalTextEdits are edits to apply elsewhere in the document when
	// this item is accepted — for TypeScript this is the auto-import line.
	// Servers omit these from the initial completion response and only fill
	// them in on completionItem/resolve, so Data must be round-tripped back
	// unchanged to obtain them. See Client.ResolveCompletion.
	AdditionalTextEdits []TextEdit `json:"additionalTextEdits,omitempty"`

	// Data is opaque server state that must be sent back unchanged via
	// completionItem/resolve to obtain AdditionalTextEdits.
	Data json.RawMessage `json:"data,omitempty"`
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// ---- textDocument/semanticTokens ----

type SemanticTokensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

// SemanticTokensResult is the raw, delta-encoded response from
// textDocument/semanticTokens/range. Data is a flat array of 5-uint32 groups
// per token: [deltaLine, deltaStartChar, length, tokenType, tokenModifiers] —
// see Client.SemanticTokensRange for the decode.
type SemanticTokensResult struct {
	ResultID string   `json:"resultId,omitempty"`
	Data     []uint32 `json:"data"`
}

// SemanticToken is one decoded token: absolute (not delta-encoded) position,
// with its type/modifiers already resolved to names via the server's legend.
// StartChar/Length are UTF-16 code-unit offsets, per LSP Position semantics.
type SemanticToken struct {
	Line      int
	StartChar int
	Length    int
	TokenType string
	Modifiers []string
}

// ---- textDocument/inlayHint ----

type InlayHintParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
}

type InlayHintKind int

const (
	InlayHintKindType      InlayHintKind = 1
	InlayHintKindParameter InlayHintKind = 2
)

// InlayHint is one hint returned by textDocument/inlayHint, e.g. the ": string"
// after an inferred variable or the "name:" before a positional argument.
type InlayHint struct {
	Position Position `json:"position"`
	// Label is json.RawMessage because the LSP spec allows two shapes: a plain
	// string, or []InlayHintLabelPart ({value, tooltip?, location?, command?}) —
	// use Text() to normalize either into a plain string.
	Label        json.RawMessage `json:"label"`
	Kind         InlayHintKind   `json:"kind,omitempty"`
	PaddingLeft  bool            `json:"paddingLeft,omitempty"`
	PaddingRight bool            `json:"paddingRight,omitempty"`
}

// Text normalizes Label to a plain display string, joining InlayHintLabelPart
// values when the server returns the structured array form.
func (h *InlayHint) Text() string {
	if h == nil || len(h.Label) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(h.Label, &s); err == nil {
		return s
	}
	var parts []struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(h.Label, &parts); err == nil {
		vals := make([]string, len(parts))
		for i, p := range parts {
			vals[i] = p.Value
		}
		return strings.Join(vals, "")
	}
	return ""
}

// ---- textDocument/definition ----

type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Location is a file URI + range returned by textDocument/definition.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// ---- workspace/symbol and textDocument/documentSymbol ----

// SymbolKind is the LSP symbol kind (1–26).
type SymbolKind int

const (
	SymbolKindFile          SymbolKind = 1
	SymbolKindModule        SymbolKind = 2
	SymbolKindNamespace     SymbolKind = 3
	SymbolKindPackage       SymbolKind = 4
	SymbolKindClass         SymbolKind = 5
	SymbolKindMethod        SymbolKind = 6
	SymbolKindProperty      SymbolKind = 7
	SymbolKindField         SymbolKind = 8
	SymbolKindConstructor   SymbolKind = 9
	SymbolKindEnum          SymbolKind = 10
	SymbolKindInterface     SymbolKind = 11
	SymbolKindFunction      SymbolKind = 12
	SymbolKindVariable      SymbolKind = 13
	SymbolKindConstant      SymbolKind = 14
	SymbolKindString        SymbolKind = 15
	SymbolKindNumber        SymbolKind = 16
	SymbolKindBoolean       SymbolKind = 17
	SymbolKindArray         SymbolKind = 18
	SymbolKindObject        SymbolKind = 19
	SymbolKindKey           SymbolKind = 20
	SymbolKindNull          SymbolKind = 21
	SymbolKindEnumMember    SymbolKind = 22
	SymbolKindStruct        SymbolKind = 23
	SymbolKindEvent         SymbolKind = 24
	SymbolKindOperator      SymbolKind = 25
	SymbolKindTypeParameter SymbolKind = 26
)

// KindLabel returns a short 2-character label for display.
func (k SymbolKind) KindLabel() string {
	switch k {
	case SymbolKindFunction:
		return "fn"
	case SymbolKindMethod:
		return "me"
	case SymbolKindClass:
		return "cl"
	case SymbolKindStruct:
		return "st"
	case SymbolKindInterface:
		return "if"
	case SymbolKindVariable:
		return "va"
	case SymbolKindConstant:
		return "co"
	case SymbolKindEnumMember:
		return "em"
	case SymbolKindEnum:
		return "en"
	case SymbolKindConstructor:
		return "ct"
	case SymbolKindProperty:
		return "pr"
	case SymbolKindField:
		return "fi"
	case SymbolKindTypeParameter:
		return "tp"
	case SymbolKindModule:
		return "mo"
	case SymbolKindPackage:
		return "pk"
	case SymbolKindNamespace:
		return "ns"
	default:
		return "  "
	}
}

// SymbolInformation is returned by workspace/symbol and (flattened) documentSymbol.
type SymbolInformation struct {
	Name          string     `json:"name"`
	Kind          SymbolKind `json:"kind"`
	ContainerName string     `json:"containerName,omitempty"`
	Location      Location   `json:"location"`
}

// WorkspaceSymbolParams is the request for workspace/symbol.
type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// DocumentSymbolParams is the request for textDocument/documentSymbol.
type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// DocumentSymbol is the hierarchical symbol returned by textDocument/documentSymbol.
// The server may return []DocumentSymbol OR []SymbolInformation; we handle both.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// ---- textDocument/references ----

type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      ReferenceContext       `json:"context"`
}

// ---- textDocument/rename ----

type RenameParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// ---- textDocument/codeAction ----

type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
	// Only restricts the response to actions whose kind is (a prefix of) one
	// of these — e.g. ["source.organizeImports"]. Omitted for the normal
	// diagnostic-driven quick-fix/refactor request, since most servers only
	// advertise source actions when a client explicitly opts in via Only.
	Only []string `json:"only,omitempty"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext      `json:"context"`
}

// TextDocumentEdit is a versioned document edit (used in documentChanges).
type TextDocumentEdit struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Edits []TextEdit `json:"edits"`
}

// WorkspaceEdit holds per-file text edits returned by a code action.
// gopls uses documentChanges; legacy servers use changes.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []TextDocumentEdit    `json:"documentChanges,omitempty"`
}

// CodeAction is a single action returned by textDocument/codeAction.
// The server may return either WorkspaceEdit-style actions or Command-style
// actions. We surface only WorkspaceEdit actions (the common case for
// quick-fixes like "remove unused import" or "add missing import").
// Command is an LSP Command: an opaque, server-defined action invoked via
// workspace/executeCommand.
type Command struct {
	Title     string            `json:"title"`
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

// CodeAction is a single action returned by textDocument/codeAction. A given
// action carries at most one of:
//   - Edit: ready to apply as-is.
//   - Data: opaque context that must be sent back unchanged via
//     codeAction/resolve to obtain the edit (e.g. some quick-fixes).
//   - Command: must be invoked via workspace/executeCommand; the server
//     then sends the edit back via a workspace/applyEdit request (e.g.
//     gopls's Extract Function/Extract Variable).
type CodeAction struct {
	Title       string          `json:"title"`
	Kind        string          `json:"kind,omitempty"`
	IsPreferred bool            `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit  `json:"edit,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Command     *Command        `json:"command,omitempty"`
}

// ---- workspace/executeCommand & workspace/applyEdit ----

type ExecuteCommandParams struct {
	Command   string            `json:"command"`
	Arguments []json.RawMessage `json:"arguments,omitempty"`
}

// ApplyWorkspaceEditParams is what a server sends the client in a
// workspace/applyEdit request (a server-to-client request, not a response).
type ApplyWorkspaceEditParams struct {
	Label string        `json:"label,omitempty"`
	Edit  WorkspaceEdit `json:"edit"`
}

// ApplyWorkspaceEditResult is the client's reply to workspace/applyEdit.
type ApplyWorkspaceEditResult struct {
	Applied bool `json:"applied"`
}
