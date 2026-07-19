package client

import (
	"context"

	proto "github.com/indiejames/indigo/internal/proto"
)

// GetPluginBindings fetches fresh plugin key bindings from the server.
func (r *RPC) GetPluginBindings(ctx context.Context) ([]ClientPluginBinding, error) {
	fut, rel := r.svc.GetPluginBindings(ctx, func(_ proto.EditorService_getPluginBindings_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Bindings()
	if err != nil {
		return nil, err
	}
	out := make([]ClientPluginBinding, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		name, _ := item.PluginName()
		key, _ := item.Key()
		desc, _ := item.Description()
		out[i] = ClientPluginBinding{PluginName: name, Key: key, Description: desc}
	}
	return out, nil
}

// HasPluginKey reports whether any plugin registered this key trigger.
func (r *RPC) HasPluginKey(key string) bool {
	r.pluginKeysMu.RLock()
	defer r.pluginKeysMu.RUnlock()
	return r.pluginKeys[key]
}

// HandlePluginKey asks the server to dispatch a keypress to the owning plugin.
func (r *RPC) HandlePluginKey(ctx context.Context, key, mode string, bufID uint32, cursorLine, cursorCol uint32) (PluginKeyResult, error) {
	select {
	case <-r.conn.Done():
		clientLog("HandlePluginKey: conn already done!")
	default:
		clientLog("HandlePluginKey: conn is alive, key=%q", key)
	}
	fut, rel := r.svc.HandlePluginKey(ctx, func(p proto.EditorService_handlePluginKey_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufId(bufID)
		p.SetCursorLine(cursorLine)
		p.SetCursorCol(cursorCol)
		if err := p.SetKey(key); err != nil {
			return err
		}
		return p.SetMode(mode)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return PluginKeyResult{}, err
	}
	result, err := res.Result()
	if err != nil {
		return PluginKeyResult{}, err
	}
	return PluginKeyResult{
		Handled:     result.Handled(),
		CursorLine:  result.CursorLine(),
		CursorCol:   result.CursorCol(),
		HasCursor:   result.HasCursor(),
		CaptureKeys: result.CaptureKeys(),
	}, nil
}

// InvokeMenuAction asks the server to dispatch a Command-menu selection to the
// plugin that registered pluginName+command via OnMenuAction.
func (r *RPC) InvokeMenuAction(ctx context.Context, pluginName, command string, bufID uint32, cursorLine, cursorCol uint32) (PluginKeyResult, error) {
	fut, rel := r.svc.InvokePluginMenuAction(ctx, func(p proto.EditorService_invokePluginMenuAction_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufId(bufID)
		p.SetCursorLine(cursorLine)
		p.SetCursorCol(cursorCol)
		if err := p.SetPluginName(pluginName); err != nil {
			return err
		}
		return p.SetCommand(command)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return PluginKeyResult{}, err
	}
	result, err := res.Result()
	if err != nil {
		return PluginKeyResult{}, err
	}
	return PluginKeyResult{
		Handled:     result.Handled(),
		CursorLine:  result.CursorLine(),
		CursorCol:   result.CursorCol(),
		HasCursor:   result.HasCursor(),
		CaptureKeys: result.CaptureKeys(),
	}, nil
}

// UpdateViewport informs the server of this client's current scroll position
// and visible height so plugins can use visibleRange accurately.
func (r *RPC) UpdateViewport(ctx context.Context, topLine, height uint32) {
	fut, rel := r.svc.UpdateViewport(ctx, func(p proto.EditorService_updateViewport_Params) error {
		p.SetClientId(r.clientID)
		p.SetTopLine(topLine)
		p.SetHeight(height)
		return nil
	})
	defer rel()
	fut.Struct() //nolint:errcheck
}

// GetDecorations fetches plugin decorations for the current client viewport.
func (r *RPC) GetDecorations(ctx context.Context, bufID uint32) ([]ClientDecoration, error) {
	fut, rel := r.svc.GetPluginDecorations(ctx, func(p proto.EditorService_getPluginDecorations_Params) error {
		p.SetClientId(r.clientID)
		p.SetBufId(bufID)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Decorations()
	if err != nil {
		return nil, err
	}
	out := make([]ClientDecoration, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		text, err := item.Text()
		if err != nil {
			return nil, err
		}
		ulColor, err := item.UnderlineColor()
		if err != nil {
			return nil, err
		}
		fixData, err := item.FixData()
		if err != nil {
			return nil, err
		}
		pluginName, err := item.PluginName()
		if err != nil {
			return nil, err
		}
		textColor, err := item.TextColor()
		if err != nil {
			return nil, err
		}
		out[i] = ClientDecoration{
			Line:           item.Line(),
			Col:            item.Col(),
			Text:           text,
			Kind:           ClientDecorationKind(item.Kind()),
			EndCol:         item.EndCol(),
			UnderlineStyle: ClientUnderlineStyle(item.UnderlineStyle()),
			UnderlineColor: ulColor,
			Fixable:        item.Fixable(),
			FixData:        fixData,
			PluginName:     pluginName,
			TextColor:      textColor,
		}
	}
	return out, nil
}

// GetPluginFixes fetches fix suggestions for a fixable decoration.
func (r *RPC) GetPluginFixes(ctx context.Context, pluginName, fixData string) ([]ClientFixItem, error) {
	fut, rel := r.svc.GetPluginFixes(ctx, func(p proto.EditorService_getPluginFixes_Params) error {
		if err := p.SetPluginName(pluginName); err != nil {
			return err
		}
		return p.SetFixData(fixData)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Items()
	if err != nil {
		return nil, err
	}
	out := make([]ClientFixItem, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		label, err := item.Label()
		if err != nil {
			return nil, err
		}
		replace, err := item.Replace()
		if err != nil {
			return nil, err
		}
		out[i] = ClientFixItem{Label: label, Replace: replace}
	}
	return out, nil
}

// ApplyPluginFix asks the plugin to apply a fix by index.
func (r *RPC) ApplyPluginFix(ctx context.Context, pluginName, fixData string, index uint32) error {
	fut, rel := r.svc.ApplyPluginFix(ctx, func(p proto.EditorService_applyPluginFix_Params) error {
		if err := p.SetPluginName(pluginName); err != nil {
			return err
		}
		if err := p.SetFixData(fixData); err != nil {
			return err
		}
		p.SetIndex(index)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// GetPluginActions fetches context-sensitive actions from all registered action providers.
func (r *RPC) GetPluginActions(ctx context.Context, bufID uint32, line, col uint32) ([]ClientFixItem, error) {
	fut, rel := r.svc.GetPluginActions(ctx, func(p proto.EditorService_getPluginActions_Params) error {
		p.SetBufId(bufID)
		p.SetLine(line)
		p.SetCol(col)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Items()
	if err != nil {
		return nil, err
	}
	out := make([]ClientFixItem, rawList.Len())
	for i := range out {
		item := rawList.At(i)
		label, _ := item.Label()
		replace, _ := item.Replace()
		pluginName, _ := item.PluginName()
		out[i] = ClientFixItem{
			Label:     label,
			Replace:   replace,
			FromLine:  int(item.FromLine()),
			FromCol:   int(item.FromCol()),
			ToLine:    int(item.ToLine()),
			ToCol:     int(item.ToCol()),
			Plugin:    pluginName,
			OrigIndex: i,
			IsAction:  true,
		}
	}
	return out, nil
}

// ApplyPluginAction asks an action provider plugin to execute a custom action.
func (r *RPC) ApplyPluginAction(ctx context.Context, pluginName string, bufID, line, col, index uint32) error {
	fut, rel := r.svc.ApplyPluginAction(ctx, func(p proto.EditorService_applyPluginAction_Params) error {
		if err := p.SetPluginName(pluginName); err != nil {
			return err
		}
		p.SetBufId(bufID)
		p.SetLine(line)
		p.SetCol(col)
		p.SetIndex(index)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// PluginPopupSelected tells the server the user picked item at index in the active plugin popup.
func (r *RPC) PluginPopupSelected(ctx context.Context, index uint32) error {
	fut, rel := r.svc.PluginPopupSelected(ctx, func(p proto.EditorService_pluginPopupSelected_Params) error {
		p.SetIndex(index)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// PluginPopupCancelled tells the server the user dismissed the plugin popup without selecting.
func (r *RPC) PluginPopupCancelled(ctx context.Context) error {
	fut, rel := r.svc.PluginPopupCancelled(ctx, nil)
	defer rel()
	_, err := fut.Struct()
	return err
}

// PluginInputConfirmed tells the server the user confirmed the input prompt with text.
func (r *RPC) PluginInputConfirmed(ctx context.Context, text string) error {
	fut, rel := r.svc.PluginInputConfirmed(ctx, func(p proto.EditorService_pluginInputConfirmed_Params) error {
		return p.SetText(text)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// PluginInputCancelled tells the server the user dismissed the input prompt.
func (r *RPC) PluginInputCancelled(ctx context.Context) error {
	fut, rel := r.svc.PluginInputCancelled(ctx, nil)
	defer rel()
	_, err := fut.Struct()
	return err
}
