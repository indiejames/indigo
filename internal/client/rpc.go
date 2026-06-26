package client

import (
	"context"
	"fmt"
	"net"

	"capnproto.org/go/capnp/v3/rpc"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// RPC wraps a Cap'n Proto connection to the editor server.
type RPC struct {
	conn     *rpc.Conn
	svc      proto.EditorService
	clientID uint64
}

// Dial connects to the server at socketPath and registers this client.
func Dial(socketPath string) (*RPC, error) {
	c, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}

	transport := rpc.NewStreamTransport(c)
	conn := rpc.NewConn(transport, nil)
	svc := proto.EditorService(conn.Bootstrap(context.Background()))

	// Register with server.
	fut, rel := svc.Connect(context.Background(), nil)
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}

	return &RPC{conn: conn, svc: svc, clientID: res.ClientId()}, nil
}

func (r *RPC) ClientID() uint64 { return r.clientID }

// OpenFile asks the server to open path and returns (bufferID, content, version, fromRecovery).
func (r *RPC) OpenFile(ctx context.Context, path string) (uint32, string, uint64, bool, error) {
	fut, rel := r.svc.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(r.clientID)
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, "", 0, false, err
	}
	content, err := res.Content()
	if err != nil {
		return 0, "", 0, false, err
	}
	return res.BufferId(), content, res.Version(), res.FromRecovery(), nil
}

// DiscardRecovery tells the server to delete the recovery file and reload the
// original file content into the buffer. Returns the original file content.
func (r *RPC) DiscardRecovery(ctx context.Context, bufID uint32) (string, error) {
	fut, rel := r.svc.DiscardRecovery(ctx, func(p proto.EditorService_discardRecovery_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return "", err
	}
	content, err := res.Content()
	if err != nil {
		return "", err
	}
	return content, nil
}

// ApplyOp sends an edit operation to the server and returns the new version.
func (r *RPC) ApplyOp(ctx context.Context, bufID uint32, op document.Op) (uint64, error) {
	fut, rel := r.svc.ApplyOp(ctx, func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		protoOp, err := p.NewOp()
		if err != nil {
			return err
		}
		protoOp.SetClientId(op.ClientID)
		protoOp.SetVersion(op.Version)
		switch op.Type {
		case document.OpInsert:
			protoOp.SetType(proto.EditOp_OpType_insert)
			protoOp.SetInsertLine(uint32(op.InsertLine))
			protoOp.SetInsertCol(uint32(op.InsertCol))
			if err := protoOp.SetInsertText(op.InsertText); err != nil {
				return err
			}
		case document.OpDelete:
			protoOp.SetType(proto.EditOp_OpType_delete)
			protoOp.SetFromLine(uint32(op.FromLine))
			protoOp.SetFromCol(uint32(op.FromCol))
			protoOp.SetToLine(uint32(op.ToLine))
			protoOp.SetToCol(uint32(op.ToCol))
		default:
			protoOp.SetType(proto.EditOp_OpType_noop)
		}
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, err
	}
	return res.Version(), nil
}

// GetUpdates polls for ops on bufID that arrived after sinceVersion.
func (r *RPC) GetUpdates(ctx context.Context, bufID uint32, since uint64) ([]document.Op, uint64, error) {
	fut, rel := r.svc.GetUpdates(ctx, func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		p.SetSinceVersion(since)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, 0, err
	}

	opList, err := res.Ops()
	if err != nil {
		return nil, 0, err
	}

	ops := make([]document.Op, opList.Len())
	for i := range ops {
		item := opList.At(i)
		insertText, _ := item.InsertText()
		op := document.Op{
			ClientID:   item.ClientId(),
			Version:    item.Version(),
			InsertLine: int(item.InsertLine()),
			InsertCol:  int(item.InsertCol()),
			InsertText: insertText,
			FromLine:   int(item.FromLine()),
			FromCol:    int(item.FromCol()),
			ToLine:     int(item.ToLine()),
			ToCol:      int(item.ToCol()),
		}
		switch item.Type() {
		case proto.EditOp_OpType_insert:
			op.Type = document.OpInsert
		case proto.EditOp_OpType_delete:
			op.Type = document.OpDelete
		default:
			op.Type = document.OpNoop
		}
		ops[i] = op
	}
	return ops, res.Version(), nil
}

// Save asks the server to flush bufID to disk.
func (r *RPC) Save(ctx context.Context, bufID uint32) error {
	fut, rel := r.svc.Save(ctx, func(p proto.EditorService_save_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// CloseBuffer signals the server that this client is done with bufID.
func (r *RPC) CloseBuffer(ctx context.Context, bufID uint32) error {
	fut, rel := r.svc.CloseBuffer(ctx, func(p proto.EditorService_closeBuffer_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// BufferClientCount returns how many clients currently have bufID open.
func (r *RPC) BufferClientCount(ctx context.Context, bufID uint32) (uint32, error) {
	fut, rel := r.svc.BufferClientCount(ctx, func(p proto.EditorService_bufferClientCount_Params) error {
		p.SetBufferId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, err
	}
	return res.Count(), nil
}

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

// GetDiagnostics fetches the current diagnostics for bufID from the server.
func (r *RPC) GetDiagnostics(ctx context.Context, bufID uint32) ([]ClientDiag, error) {
	fut, rel := r.svc.GetDiagnostics(ctx, func(p proto.EditorService_getDiagnostics_Params) error {
		p.SetBufId(bufID)
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
	return out, nil
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

// Disconnect unregisters this client from the server.
func (r *RPC) Disconnect(ctx context.Context) error {
	fut, rel := r.svc.Disconnect(ctx, func(p proto.EditorService_disconnect_Params) error {
		p.SetClientId(r.clientID)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	r.svc.Release()
	r.conn.Close()
	return err
}
