package client

import (
	"context"

	proto "github.com/indiejames/indigo/internal/proto"
)

// ClientDiag is a diagnostic delivered from the language server.
type ClientDiag struct {
	Line, Col, EndLine, EndCol int
	Severity                   uint8
	Message, Source            string
}

// ClientHoverResult is the result of a hover request.
type ClientHoverResult struct {
	Found    bool
	Contents string
}

// ClientSigParam is one parameter in a signature.
type ClientSigParam struct{ Label string }

// ClientSigInfo is one signature returned by signatureHelp.
type ClientSigInfo struct {
	Label         string
	Documentation string
	Params        []ClientSigParam
}

// ClientSigHelp is the result of a signatureHelp request.
type ClientSigHelp struct {
	Signatures      []ClientSigInfo
	ActiveSignature int
	ActiveParameter int
}

// ClientCompletion is one completion item.
type ClientCompletion struct {
	Label, Detail, InsertText string
	Kind                      uint8
	// SortText/FilterText drive client-side ranking and filtering; FilterText
	// falls back to Label when empty.
	SortText, FilterText string
	// TextEdit, when non-nil, is the authoritative primary edit for accepting
	// this item (its range may cover more than the typed prefix). Preferred over
	// InsertText/Label.
	TextEdit *ClientLspEdit
	// Data is the opaque resolve token; pass it back to ResolveCompletion to
	// obtain AdditionalEdits. Empty when the server has no resolve data.
	Data []byte
	// AdditionalEdits are edits to apply elsewhere when this item is accepted
	// (the auto-import line). Empty until ResolveCompletion fills them in.
	AdditionalEdits []ClientLspEdit
	// Source is empty for a language-server-provided item, or the name of the
	// plugin that supplied it. Round-tripped through ResolveCompletion so the
	// server knows whether to resolve via the language server or the plugin.
	Source string
}

// DiagnosticsResult bundles diagnostics with the server's lspReady flag.
type DiagnosticsResult struct {
	Diags    []ClientDiag
	LspReady bool
}

func (r *RPC) GetDiagnostics(ctx context.Context, bufID uint32) (DiagnosticsResult, error) {
	fut, rel := r.svc.GetDiagnostics(ctx, func(p proto.EditorService_getDiagnostics_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return DiagnosticsResult{}, err
	}
	list, err := res.Items()
	if err != nil {
		return DiagnosticsResult{}, err
	}
	out := make([]ClientDiag, list.Len())
	for i := range out {
		it := list.At(i)
		msg, _ := it.Message_()
		src, _ := it.Source()
		out[i] = ClientDiag{
			Line: int(it.Line()), Col: int(it.Col()),
			EndLine: int(it.EndLine()), EndCol: int(it.EndCol()),
			Severity: it.Severity(),
			Message:  msg,
			Source:   src,
		}
	}
	return DiagnosticsResult{Diags: out, LspReady: res.LspReady()}, nil
}

// Hover fetches hover information for bufID at (line, col).
func (r *RPC) Hover(ctx context.Context, bufID uint32, line, col int) (ClientHoverResult, error) {
	fut, rel := r.svc.Hover(ctx, func(p proto.EditorService_hover_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientHoverResult{}, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientHoverResult{}, err
	}
	contents, _ := result.Contents()
	return ClientHoverResult{Found: result.Found(), Contents: contents}, nil
}

// SignatureHelp fetches signature help for bufID at (line, col).
func (r *RPC) SignatureHelp(ctx context.Context, bufID uint32, line, col int) (ClientSigHelp, error) {
	fut, rel := r.svc.SignatureHelp(ctx, func(p proto.EditorService_signatureHelp_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientSigHelp{}, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientSigHelp{}, err
	}
	if !result.Found() {
		return ClientSigHelp{}, nil
	}
	sh := ClientSigHelp{
		ActiveSignature: int(result.ActiveSignature()),
		ActiveParameter: int(result.ActiveParameter()),
	}
	sigs, err := result.Signatures()
	if err != nil {
		return sh, nil
	}
	sh.Signatures = make([]ClientSigInfo, sigs.Len())
	for i := range sh.Signatures {
		s := sigs.At(i)
		label, _ := s.Label()
		doc, _ := s.Documentation()
		si := ClientSigInfo{Label: label, Documentation: doc}
		params, err := s.Parameters()
		if err == nil {
			si.Params = make([]ClientSigParam, params.Len())
			for j := range si.Params {
				pl, _ := params.At(j).Label()
				si.Params[j] = ClientSigParam{Label: pl}
			}
		}
		sh.Signatures[i] = si
	}
	return sh, nil
}

// Complete fetches completion items for bufID at (line, col).
func (r *RPC) Complete(ctx context.Context, bufID uint32, line, col int) ([]ClientCompletion, error) {
	fut, rel := r.svc.Complete(ctx, func(p proto.EditorService_complete_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Items()
	if err != nil {
		return nil, err
	}
	out := make([]ClientCompletion, list.Len())
	for i := range out {
		out[i] = completionFromProto(list.At(i))
	}
	return out, nil
}

// completionFromProto decodes a proto CompletionItem, copying the opaque data
// token and any additionalTextEdits out of the capnp message so they stay valid
// after it's released.
func completionFromProto(it proto.CompletionItem) ClientCompletion {
	label, _ := it.Label()
	detail, _ := it.Detail()
	insert, _ := it.InsertText()
	sortText, _ := it.SortText()
	filterText, _ := it.FilterText()
	source, _ := it.Source()
	c := ClientCompletion{
		Label: label, Detail: detail, InsertText: insert, Kind: it.Kind(),
		SortText: sortText, FilterText: filterText, Source: source,
	}
	if data, err := it.Data(); err == nil && len(data) > 0 {
		c.Data = append([]byte(nil), data...)
	}
	if it.HasTextEdit() {
		if te, err := it.TextEdit(); err == nil {
			nt, _ := te.NewText()
			c.TextEdit = &ClientLspEdit{
				FromLine: int(te.FromLine()), FromCol: int(te.FromCol()),
				ToLine: int(te.ToLine()), ToCol: int(te.ToCol()),
				NewText: nt,
			}
		}
	}
	if edits, err := it.AdditionalTextEdits(); err == nil && edits.Len() > 0 {
		c.AdditionalEdits = make([]ClientLspEdit, edits.Len())
		for j := range c.AdditionalEdits {
			e := edits.At(j)
			nt, _ := e.NewText()
			c.AdditionalEdits[j] = ClientLspEdit{
				FromLine: int(e.FromLine()),
				FromCol:  int(e.FromCol()),
				ToLine:   int(e.ToLine()),
				ToCol:    int(e.ToCol()),
				NewText:  nt,
			}
		}
	}
	return c
}

// ResolveCompletion resolves item (as returned by Complete) for bufID, filling
// in AdditionalEdits — the auto-import line for a symbol from another module.
// The item's Data token is sent back unchanged so the language server (or, for
// a plugin-sourced item, the plugin) can identify which candidate to resolve.
// The outbound request does not carry item's TextEdit (only the fields set
// below), so if the resolved response doesn't supply its own — a plugin
// resolver has no way to reconstruct one it was never given, unlike a
// language server, which can often derive it from its own cached state — the
// pre-resolve TextEdit is retained rather than silently dropped; applying the
// accepted item with no TextEdit at all falls back to replacing only the
// typed-prefix range, which under-replaces anything a TextEdit covered beyond
// that (e.g. a semver range operator prefix). Returns the item unchanged on
// error.
func (r *RPC) ResolveCompletion(ctx context.Context, bufID uint32, item ClientCompletion) (ClientCompletion, error) {
	fut, rel := r.svc.ResolveCompletion(ctx, func(p proto.EditorService_resolveCompletion_Params) error {
		p.SetBufId(bufID)
		ci, err := p.NewItem()
		if err != nil {
			return err
		}
		if err := ci.SetLabel(item.Label); err != nil {
			return err
		}
		ci.SetKind(item.Kind)
		if err := ci.SetDetail(item.Detail); err != nil {
			return err
		}
		if err := ci.SetInsertText(item.InsertText); err != nil {
			return err
		}
		if err := ci.SetSource(item.Source); err != nil {
			return err
		}
		if len(item.Data) > 0 {
			if err := ci.SetData(item.Data); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()

	res, err := fut.Struct()
	if err != nil {
		return item, err
	}
	out, err := res.Item()
	if err != nil {
		return item, err
	}
	resolved := completionFromProto(out)
	if resolved.TextEdit == nil && item.TextEdit != nil {
		resolved.TextEdit = item.TextEdit
	}
	return resolved, nil
}

// ClientLocation is a resolved definition location.
type ClientLocation struct {
	Path string
	Line int
	Col  int
}

// Definition fetches the definition location for the symbol at (line, col) in bufID.
func (r *RPC) Definition(ctx context.Context, bufID uint32, line, col int) (ClientLocation, bool, error) {
	fut, rel := r.svc.Definition(ctx, func(p proto.EditorService_definition_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ClientLocation{}, false, err
	}
	result, err := res.Result()
	if err != nil {
		return ClientLocation{}, false, err
	}
	if !result.Found() {
		return ClientLocation{}, false, nil
	}
	path, _ := result.Path()
	return ClientLocation{
		Path: path,
		Line: int(result.Line()),
		Col:  int(result.Col()),
	}, true, nil
}

// ClientSymbol is a symbol result from workspace/symbol or documentSymbol.
type ClientSymbol struct {
	Name          string
	Kind          uint8
	KindLabel     string
	ContainerName string
	Path          string
	Line          int
	Col           int
}

// ClientReference is one location in a find-references result.
type ClientReference struct {
	Path    string
	Line    int
	Col     int
	Preview string
}

// kindLabel returns a short 2-character label for an LSP symbol kind value.
func kindLabel(k uint8) string {
	switch k {
	case 12: // Function
		return "fn"
	case 6: // Method
		return "me"
	case 5: // Class
		return "cl"
	case 23: // Struct
		return "st"
	case 11: // Interface
		return "if"
	case 13: // Variable
		return "va"
	case 14: // Constant
		return "co"
	case 22: // EnumMember
		return "em"
	case 10: // Enum
		return "en"
	case 9: // Constructor
		return "ct"
	case 7: // Property
		return "pr"
	case 8: // Field
		return "fi"
	case 26: // TypeParameter
		return "tp"
	case 2: // Module
		return "mo"
	case 4: // Package
		return "pk"
	case 3: // Namespace
		return "ns"
	default:
		return "  "
	}
}

// References fetches all reference locations for the symbol at (line, col) in bufID.
func (r *RPC) References(ctx context.Context, bufID uint32, line, col int) ([]ClientReference, error) {
	fut, rel := r.svc.References(ctx, func(p proto.EditorService_references_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Locations()
	if err != nil {
		return nil, err
	}
	out := make([]ClientReference, list.Len())
	for i := range out {
		fl := list.At(i)
		path, _ := fl.Path()
		preview, _ := fl.Preview()
		out[i] = ClientReference{
			Path:    path,
			Line:    int(fl.Line()),
			Col:     int(fl.Col()),
			Preview: preview,
		}
	}
	return out, nil
}

// WorkspaceSymbols queries the server for workspace symbols matching query.
// bufID is used as a hint to select the right language server.
func (r *RPC) WorkspaceSymbols(ctx context.Context, bufID uint32, query string) ([]ClientSymbol, error) {
	fut, rel := r.svc.WorkspaceSymbols(ctx, func(p proto.EditorService_workspaceSymbols_Params) error {
		p.SetBufId(bufID)
		return p.SetQuery(query)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Symbols()
	if err != nil {
		return nil, err
	}
	out := make([]ClientSymbol, list.Len())
	for i := range out {
		sr := list.At(i)
		name, _ := sr.Name()
		container, _ := sr.ContainerName()
		path, _ := sr.Path()
		kind := sr.Kind()
		out[i] = ClientSymbol{
			Name:          name,
			Kind:          kind,
			KindLabel:     kindLabel(kind),
			ContainerName: container,
			Path:          path,
			Line:          int(sr.Line()),
			Col:           int(sr.Col()),
		}
	}
	return out, nil
}

// DocumentSymbols fetches symbols for the document in bufID.
func (r *RPC) DocumentSymbols(ctx context.Context, bufID uint32) ([]ClientSymbol, error) {
	fut, rel := r.svc.DocumentSymbols(ctx, func(p proto.EditorService_documentSymbols_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Symbols()
	if err != nil {
		return nil, err
	}
	out := make([]ClientSymbol, list.Len())
	for i := range out {
		sr := list.At(i)
		name, _ := sr.Name()
		container, _ := sr.ContainerName()
		path, _ := sr.Path()
		kind := sr.Kind()
		out[i] = ClientSymbol{
			Name:          name,
			Kind:          kind,
			KindLabel:     kindLabel(kind),
			ContainerName: container,
			Path:          path,
			Line:          int(sr.Line()),
			Col:           int(sr.Col()),
		}
	}
	return out, nil
}

// Format requests server-side formatting for bufID.
func (r *RPC) Format(ctx context.Context, bufID uint32) (content string, changed bool, noFormatter bool, err error) {
	fut, rel := r.svc.Format(ctx, func(p proto.EditorService_format_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", false, false, err
	}
	content, err = res.Content()
	if err != nil {
		return "", false, false, err
	}
	return content, res.Changed(), res.NoFormatter(), nil
}

// ClientInlayHint is an inlay hint (inferred type or parameter name) to render
// as virtual text at a position — never part of the actual buffer content.
type ClientInlayHint struct {
	Line, Col                 int
	Label                     string
	Kind                      uint8 // 1 = Type, 2 = Parameter
	PaddingLeft, PaddingRight bool
}

// InlayHints fetches inlay hints for bufID within [startLine,endLine) —
// normally the client's visible viewport, not the whole file.
func (r *RPC) InlayHints(ctx context.Context, bufID uint32, startLine, startCol, endLine, endCol int) ([]ClientInlayHint, error) {
	fut, rel := r.svc.InlayHints(ctx, func(p proto.EditorService_inlayHints_Params) error {
		p.SetBufId(bufID)
		p.SetStartLine(uint32(startLine))
		p.SetStartCol(uint32(startCol))
		p.SetEndLine(uint32(endLine))
		p.SetEndCol(uint32(endCol))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Hints()
	if err != nil {
		return nil, err
	}
	out := make([]ClientInlayHint, list.Len())
	for i := range out {
		h := list.At(i)
		label, _ := h.Label()
		out[i] = ClientInlayHint{
			Line: int(h.Line()), Col: int(h.Col()),
			Label: label, Kind: h.Kind(),
			PaddingLeft: h.PaddingLeft(), PaddingRight: h.PaddingRight(),
		}
	}
	return out, nil
}

// ClientSemanticToken is one LSP-derived syntax-coloring token. Col/Length are
// already rune-based (converted server-side from the LSP spec's UTF-16
// code-unit offsets), and TokenType/Modifiers are already resolved from the
// server's legend — the client never needs to know about either.
type ClientSemanticToken struct {
	Line, Col, Length int
	TokenType         string
	Modifiers         []string
}

// SemanticTokensRange fetches LSP-derived syntax-coloring tokens for bufID
// within [startLine,endLine) — normally the client's visible viewport, not
// the whole file.
func (r *RPC) SemanticTokensRange(ctx context.Context, bufID uint32, startLine, startCol, endLine, endCol int) ([]ClientSemanticToken, error) {
	fut, rel := r.svc.SemanticTokensRange(ctx, func(p proto.EditorService_semanticTokensRange_Params) error {
		p.SetBufId(bufID)
		p.SetStartLine(uint32(startLine))
		p.SetStartCol(uint32(startCol))
		p.SetEndLine(uint32(endLine))
		p.SetEndCol(uint32(endCol))
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Tokens()
	if err != nil {
		return nil, err
	}
	out := make([]ClientSemanticToken, list.Len())
	for i := range out {
		tok := list.At(i)
		tokenType, _ := tok.TokenType()
		out[i] = ClientSemanticToken{
			Line: int(tok.Line()), Col: int(tok.Col()), Length: int(tok.Length()),
			TokenType: tokenType,
		}
		if mods, err := tok.Modifiers(); err == nil && mods.Len() > 0 {
			out[i].Modifiers = make([]string, mods.Len())
			for j := range out[i].Modifiers {
				out[i].Modifiers[j], _ = mods.At(j)
			}
		}
	}
	return out, nil
}

// ClientLspEdit is a single text replacement from an LSP code action.
type ClientLspEdit struct {
	FromLine, FromCol int
	ToLine, ToCol     int
	NewText           string
}

// LspRename renames the symbol at (line, col) via the language server,
// applying the resulting edits across every affected file server-side.
// Returns how many edits were applied and across how many files; both are 0
// if there's no language server for this buffer, it doesn't support
// renaming, or there was nothing to rename.
func (r *RPC) LspRename(ctx context.Context, bufID uint32, line, col int, newName string) (applied, files int, err error) {
	fut, rel := r.svc.LspRename(ctx, func(p proto.EditorService_lspRename_Params) error {
		p.SetClientId(0) // see ApplyWorkspaceEdits: 0 so this client's own open tabs still refresh
		p.SetBufId(bufID)
		p.SetLine(uint32(line))
		p.SetCol(uint32(col))
		return p.SetNewName(newName)
	})
	defer rel()

	res, err := fut.Struct()
	if err != nil {
		return 0, 0, err
	}
	return int(res.AppliedCount()), int(res.FileCount()), nil
}

// ClientLspCodeAction is a code action returned by the LSP server.
type ClientLspCodeAction struct {
	Title string
	Kind  string
	Edits []ClientLspEdit
}

// LspCodeActions fetches quick-fix and refactor actions for the given range.
// startLine/startCol and endLine/endCol may be equal (a plain cursor
// position) or span a selection, letting the server offer range-only
// actions like Extract Function/Extract Variable too.
func (r *RPC) LspCodeActions(ctx context.Context, bufID uint32, startLine, startCol, endLine, endCol int) ([]ClientLspCodeAction, error) {
	fut, rel := r.svc.LspCodeActions(ctx, func(p proto.EditorService_lspCodeActions_Params) error {
		p.SetBufId(bufID)
		p.SetLine(uint32(startLine))
		p.SetCol(uint32(startCol))
		p.SetEndLine(uint32(endLine))
		p.SetEndCol(uint32(endCol))
		return nil
	})
	defer rel()

	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Actions()
	if err != nil {
		return nil, err
	}
	out := make([]ClientLspCodeAction, list.Len())
	for i := range out {
		item := list.At(i)
		title, _ := item.Title()
		edits, _ := item.Edits()
		editList := make([]ClientLspEdit, edits.Len())
		for j := range editList {
			e := edits.At(j)
			nt, _ := e.NewText()
			editList[j] = ClientLspEdit{
				FromLine: int(e.FromLine()),
				FromCol:  int(e.FromCol()),
				ToLine:   int(e.ToLine()),
				ToCol:    int(e.ToCol()),
				NewText:  nt,
			}
		}
		kind, _ := item.Kind()
		out[i] = ClientLspCodeAction{Title: title, Kind: kind, Edits: editList}
	}
	return out, nil
}

// LspOrganizeImports requests "source.organizeImports" from bufId's
// language server over the whole file and returns the resulting edits (if
// any), for the caller to apply through the normal undo-aware batch path
// like any other LSP edit. Empty means no language server, no
// organize-imports support, or nothing to change.
func (r *RPC) LspOrganizeImports(ctx context.Context, bufID uint32) ([]ClientLspEdit, error) {
	fut, rel := r.svc.LspOrganizeImports(ctx, func(p proto.EditorService_lspOrganizeImports_Params) error {
		p.SetBufId(bufID)
		return nil
	})
	defer rel()

	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	edits, err := res.Edits()
	if err != nil {
		return nil, err
	}
	out := make([]ClientLspEdit, edits.Len())
	for i := range out {
		e := edits.At(i)
		nt, _ := e.NewText()
		out[i] = ClientLspEdit{
			FromLine: int(e.FromLine()),
			FromCol:  int(e.FromCol()),
			ToLine:   int(e.ToLine()),
			ToCol:    int(e.ToCol()),
			NewText:  nt,
		}
	}
	return out, nil
}
