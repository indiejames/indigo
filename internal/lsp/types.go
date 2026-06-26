package lsp

import "fmt"

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

type TextDocumentSyncOptions struct {
	OpenClose bool                 `json:"openClose"`
	Change    TextDocumentSyncKind `json:"change"`
	Save      *SaveOptions         `json:"save,omitempty"`
}

type SaveOptions struct {
	IncludeText bool `json:"includeText"`
}

type ServerCapabilities struct {
	TextDocumentSync   *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	HoverProvider      any                      `json:"hoverProvider,omitempty"`
	SignatureHelpProvider *SignatureHelpOptions  `json:"signatureHelpProvider,omitempty"`
	CompletionProvider *CompletionOptions        `json:"completionProvider,omitempty"`
}

type SignatureHelpOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
}

type InitializeParams struct {
	ProcessID  int        `json:"processId"`
	ClientInfo ClientInfo `json:"clientInfo"`
	RootURI    string     `json:"rootUri"`
	Capabilities struct{} `json:"capabilities"`
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
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

func (h *Hover) Text() string {
	if h == nil {
		return ""
	}
	return h.Contents.Value
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
