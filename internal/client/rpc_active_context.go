package client

import (
	"context"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// ActiveContext describes the most recently active buffer across all editor clients.
type ActiveContext struct {
	Found     bool
	ClientID  uint64
	BufID     uint32
	FilePath  string
	Line      uint32
	Col       uint32
	UpdatedAt time.Time
}

// ActiveSelection describes the current editor selection, in document order
// with an inclusive end column. Found=false means nothing is selected.
type ActiveSelection struct {
	Found     bool
	BufID     uint32
	StartLine uint32
	StartCol  uint32
	EndLine   uint32
	EndCol    uint32
	IsLine    bool
}

// SetActiveSelection reports this client's selection (or clears it when
// active=false). Coordinates must be in document order, end column inclusive.
func (r *RPC) SetActiveSelection(ctx context.Context, clientID uint64, bufID uint32, sel ActiveSelection) error {
	fut, rel := r.svc.SetActiveSelection(ctx, func(p proto.EditorService_setActiveSelection_Params) error {
		p.SetClientId(clientID)
		p.SetBufId(bufID)
		p.SetStartLine(sel.StartLine)
		p.SetStartCol(sel.StartCol)
		p.SetEndLine(sel.EndLine)
		p.SetEndCol(sel.EndCol)
		p.SetIsLine(sel.IsLine)
		p.SetActive(sel.Found)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// GetActiveSelection returns the most recently reported editor selection.
func (r *RPC) GetActiveSelection(ctx context.Context) (ActiveSelection, error) {
	fut, rel := r.svc.GetActiveSelection(ctx, func(proto.EditorService_getActiveSelection_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ActiveSelection{}, err
	}
	if !res.Found() {
		return ActiveSelection{}, nil
	}
	return ActiveSelection{
		Found:     true,
		BufID:     res.BufId(),
		StartLine: res.StartLine(),
		StartCol:  res.StartCol(),
		EndLine:   res.EndLine(),
		EndCol:    res.EndCol(),
		IsLine:    res.IsLine(),
	}, nil
}

// SetActiveContext notifies the server that this client's cursor/edit position
// is the current active context. Call on buffer switch, cursor move, or any edit.
func (r *RPC) SetActiveContext(ctx context.Context, clientID uint64, bufID uint32, filePath string, line, col uint32) error {
	fut, rel := r.svc.SetActiveContext(ctx, func(p proto.EditorService_setActiveContext_Params) error {
		p.SetClientId(clientID)
		p.SetBufId(bufID)
		if err := p.SetFilePath(filePath); err != nil {
			return err
		}
		p.SetLine(line)
		p.SetCol(col)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// GetActiveContext returns the server's current active context (last editor that called SetActiveContext).
func (r *RPC) GetActiveContext(ctx context.Context) (ActiveContext, error) {
	fut, rel := r.svc.GetActiveContext(ctx, func(proto.EditorService_getActiveContext_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return ActiveContext{}, err
	}
	result, err := res.Result()
	if err != nil {
		return ActiveContext{}, err
	}
	if !result.Found() {
		return ActiveContext{}, nil
	}
	fp, err := result.FilePath()
	if err != nil {
		return ActiveContext{}, err
	}
	return ActiveContext{
		Found:     true,
		ClientID:  result.ClientId(),
		BufID:     result.BufId(),
		FilePath:  fp,
		Line:      result.Line(),
		Col:       result.Col(),
		UpdatedAt: time.Unix(0, result.UpdatedAt()),
	}, nil
}
