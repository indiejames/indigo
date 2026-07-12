package client

import (
	"context"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

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

// SaveAs writes the buffer to newPath and updates the server's path record.
func (r *RPC) SaveAs(ctx context.Context, bufID uint32, newPath string) error {
	fut, rel := r.svc.SaveAs(ctx, func(p proto.EditorService_saveAs_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		return p.SetPath(newPath)
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
