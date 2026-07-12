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
	TextDocumentSync           json.RawMessage `json:"textDocumentSync,omitempty"`
	HoverProvider              any             `json:"hoverProvider,omitempty"`
	SignatureHelpProvider      any             `json:"signatureHelpProvider,omitempty"`
	CompletionProvider         any             `json:"completionProvider,omitempty"`
	DocumentFormattingProvider any             `json:"documentFormattingProvider,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type InitializeParams struct {
	ProcessID            int        `json:"processId"`
	ClientInfo           ClientInfo `json:"clientInfo"`
	RootURI              string     `json:"rootUri"`
	Capabilities         struct{}   `json:"capabilities"`
	InitializationOptions map[string]any `json:"initializationOptions,omitempty"`
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
	TextDocument   VersionedTextDocumentIdentifier   `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent  `json:"contentChanges"`
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
	URI         string       `json:"uri"`
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
	ActiveSignature int                   `json:"activeSignature"`
	ActiveParameter int                   `json:"activeParameter"`
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
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
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

// ---- textDocument/codeAction ----

type CodeActionContext struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      CodeActionContext       `json:"context"`
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
type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}
