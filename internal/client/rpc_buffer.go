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

// encodeOp writes a document.Op into a wire EditOp.
func encodeOp(protoOp proto.EditOp, op document.Op) error {
	protoOp.SetClientId(op.ClientID)
	protoOp.SetVersion(op.Version)
	switch op.Type {
	case document.OpInsert:
		protoOp.SetType(proto.EditOp_OpType_insert)
		protoOp.SetInsertLine(uint32(op.InsertLine))
		protoOp.SetInsertCol(uint32(op.InsertCol))
		return protoOp.SetInsertText(op.InsertText)
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
		return encodeOp(protoOp, op)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, err
	}
	return res.Version(), nil
}

// ApplyOps sends a batch of edit operations in one request. The server applies
// the whole batch even if this client dies mid-call, so paired ops (e.g. a
// delete+insert replace) can never be left half-applied.
func (r *RPC) ApplyOps(ctx context.Context, bufID uint32, ops []document.Op) (uint64, error) {
	fut, rel := r.svc.ApplyOps(ctx, func(p proto.EditorService_applyOps_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		list, err := p.NewOps(int32(len(ops)))
		if err != nil {
			return err
		}
		for i, op := range ops {
			if err := encodeOp(list.At(i), op); err != nil {
				return err
			}
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

// WorkspaceEdit is a single verified text replacement within one file, used
// by ApplyWorkspaceEdits for global search-and-replace: the server applies it
// only if OldText still matches the file's current content at (Line, Col).
type WorkspaceEdit struct {
	Path    string
	Line    int
	Col     int
	OldText string
	NewText string
}

// ApplyWorkspaceEdits sends a batch of workspace-wide search-and-replace
// edits. Returns how many were applied and the indices into edits that were
// skipped because OldText no longer matched (a concurrent change since the
// edit was queued).
func (r *RPC) ApplyWorkspaceEdits(ctx context.Context, edits []WorkspaceEdit) (applied int, skippedIdx []int, err error) {
	fut, rel := r.svc.ApplyWorkspaceEdits(ctx, func(p proto.EditorService_applyWorkspaceEdits_Params) error {
		// clientID 0 (never assigned to a real connection) rather than
		// r.clientID: a "replace all" can touch a file this same client
		// already has open as a live tab, and that tab's own poll loop
		// filters out ops whose ClientID matches its own — using our real ID
		// here would leave such a tab showing stale content forever.
		p.SetClientId(0)
		list, err := p.NewEdits(int32(len(edits)))
		if err != nil {
			return err
		}
		for i, e := range edits {
			item := list.At(i)
			if err := item.SetPath(e.Path); err != nil {
				return err
			}
			item.SetLine(uint32(e.Line))
			item.SetCol(uint32(e.Col))
			if err := item.SetOldText(e.OldText); err != nil {
				return err
			}
			if err := item.SetNewText(e.NewText); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return 0, nil, err
	}
	idxList, err := res.SkippedIdx()
	if err != nil {
		return int(res.AppliedCount()), nil, nil
	}
	skipped := make([]int, idxList.Len())
	for i := range skipped {
		skipped[i] = int(idxList.At(i))
	}
	return int(res.AppliedCount()), skipped, nil
}

// GetUpdates polls for ops on bufID that arrived after sinceVersion.
func (r *RPC) GetUpdates(ctx context.Context, bufID uint32, since uint64) ([]document.Op, uint64, []byte, error) {
	fut, rel := r.svc.GetUpdates(ctx, func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufferId(bufID)
		p.SetSinceVersion(since)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, 0, nil, err
	}
	savedHash, _ := res.SavedHash()

	opList, err := res.Ops()
	if err != nil {
		return nil, 0, nil, err
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
	return ops, res.Version(), savedHash, nil
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

// RequestOpenFile asks every connected client to navigate to path at a
// 0-based line, the same broadcast Go-native plugins get via
// ServerBridge.PluginOpenFile.
func (r *RPC) RequestOpenFile(ctx context.Context, path string, line uint32) error {
	fut, rel := r.svc.RequestOpenFile(ctx, func(p proto.EditorService_requestOpenFile_Params) error {
		p.SetLine(line)
		return p.SetPath(path)
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
