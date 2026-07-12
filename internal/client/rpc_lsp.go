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
		it := list.At(i)
		label, _ := it.Label()
		detail, _ := it.Detail()
		insert, _ := it.InsertText()
		out[i] = ClientCompletion{Label: label, Detail: detail, InsertText: insert, Kind: it.Kind()}
	}
	return out, nil
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
