package client

import (
	"context"

	proto "github.com/indiejames/indigo/internal/proto"
)

// SetStatusBarText contributes a named text segment to the editor's status bar.
// The key identifies this client's contribution; passing an empty text removes it.
// The text appears in the status bar of all editor windows for this workspace.
func (r *RPC) SetStatusBarText(ctx context.Context, key, text string) error {
	fut, rel := r.svc.SetStatusBarText(ctx, func(p proto.EditorService_setStatusBarText_Params) error {
		if err := p.SetKey(key); err != nil {
			return err
		}
		return p.SetText(text)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}
