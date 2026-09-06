package server

import (
	"context"
	"time"

	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// pluginReadyTimeout bounds how long GetMenuItems/GetPluginBindings wait for
// plugin startup to finish before returning whatever is available so far.
const pluginReadyTimeout = 2 * time.Second

func (s *editorService) HandlePluginKey(ctx context.Context, call proto.EditorService_handlePluginKey) error {
	args := call.Args()
	key, err := args.Key()
	if err != nil {
		return err
	}
	mode, err := args.Mode()
	if err != nil {
		return err
	}

	clientID := args.ClientId()
	bufID := args.BufId()
	cursorLine := args.CursorLine()
	cursorCol := args.CursorCol()
	handled, edits, cursorLine, cursorCol, hasCursor, captureKeys, handleErr := s.pluginMgr.HandleKey(ctx, key, mode, bufID, clientID, cursorLine, cursorCol)
	if handleErr != nil {
		return handleErr
	}

	// Apply edits returned by the plugin to the server buffer.
	// The plugin may have also called applyEdit directly; these cover the
	// case where the plugin returns edits atomically via KeyResponse.
	if handled && len(edits) > 0 {
		// edits reference a buffer by position but KeyResponse doesn't carry bufID;
		// we apply them via the bridge using the active buffer is ambiguous here.
		// For now, skip auto-application — plugins should call applyEdit explicitly.
		_ = edits
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetHandled(handled)
	result.SetHasCursor(hasCursor)
	if hasCursor {
		result.SetCursorLine(cursorLine)
		result.SetCursorCol(cursorCol)
	}
	result.SetCaptureKeys(captureKeys)
	return nil
}

func (s *editorService) GetPluginDecorations(ctx context.Context, call proto.EditorService_getPluginDecorations) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufId()

	s.mu.Lock()
	ce, ok := s.clientMap[clientID]
	var topLine, height uint32
	if ok {
		topLine = ce.topLine
		height = ce.height
	}
	s.mu.Unlock()

	var endLine uint32
	if height > 0 {
		endLine = topLine + height - 1
	}
	decorations := s.pluginMgr.GetDecorations(ctx, clientID, bufID, topLine, endLine)
	decorations = append(decorations, s.statusBar.asDecorations()...)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewDecorations(int32(len(decorations)))
	if err != nil {
		return err
	}
	for i, d := range decorations {
		item := list.At(i)
		item.SetLine(d.Line)
		item.SetCol(d.Col)
		item.SetEndCol(d.EndCol)
		if err := item.SetText(d.Text); err != nil {
			return err
		}
		item.SetKind(proto.PluginDecorationKind(d.Kind))
		item.SetUnderlineStyle(proto.PluginUnderlineStyle(d.UnderlineStyle))
		if err := item.SetUnderlineColor(d.UnderlineColor); err != nil {
			return err
		}
		item.SetFixable(d.Fixable)
		if err := item.SetFixData(d.FixData); err != nil {
			return err
		}
		if err := item.SetPluginName(d.PluginName); err != nil {
			return err
		}
		if err := item.SetTextColor(d.TextColor); err != nil {
			return err
		}
		item.SetOldLine(d.OldLine)
	}
	return nil
}

func (s *editorService) GetPluginFixes(ctx context.Context, call proto.EditorService_getPluginFixes) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	fixData, err := args.FixData()
	if err != nil {
		return err
	}
	items, err := s.pluginMgr.GetFixes(ctx, pluginName, fixData)
	if err != nil {
		return err
	}
	res, resErr := call.AllocResults()
	if resErr != nil {
		return resErr
	}
	list, listErr := res.NewItems(int32(len(items)))
	if listErr != nil {
		return listErr
	}
	for i, it := range items {
		fi := list.At(i)
		if err := fi.SetLabel(it.Label); err != nil {
			return err
		}
		if err := fi.SetReplace(it.Replace); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) ApplyPluginFix(ctx context.Context, call proto.EditorService_applyPluginFix) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	fixData, err := args.FixData()
	if err != nil {
		return err
	}
	index := args.Index()
	if err := s.pluginMgr.ApplyFix(ctx, pluginName, fixData, index); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorService) GetPluginActions(ctx context.Context, call proto.EditorService_getPluginActions) error {
	args := call.Args()
	bufID := args.BufId()
	line := args.Line()
	col := args.Col()
	actions, err := s.pluginMgr.GetActionsAt(ctx, bufID, line, col)
	if err != nil {
		return err
	}
	res, resErr := call.AllocResults()
	if resErr != nil {
		return resErr
	}
	list, listErr := res.NewItems(int32(len(actions)))
	if listErr != nil {
		return listErr
	}
	for i, a := range actions {
		it := list.At(i)
		if err := it.SetLabel(a.Label); err != nil {
			return err
		}
		if err := it.SetReplace(a.Replace); err != nil {
			return err
		}
		it.SetFromLine(a.FromLine)
		it.SetFromCol(a.FromCol)
		it.SetToLine(a.ToLine)
		it.SetToCol(a.ToCol)
		if err := it.SetPluginName(a.PluginName); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) ApplyPluginAction(ctx context.Context, call proto.EditorService_applyPluginAction) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	if err := s.pluginMgr.ApplyAction(ctx, pluginName, args.BufId(), args.Line(), args.Col(), args.Index()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

// PluginPopupSelected implements proto.EditorService_Server.
// The focused client selected an item in the plugin popup at the given index.
func (s *editorService) PluginPopupSelected(_ context.Context, call proto.EditorService_pluginPopupSelected) error {
	index := call.Args().Index()
	s.mu.Lock()
	fn := s.popupOnSelect
	items := s.popupItems
	s.popupOnSelect = nil
	s.popupOnCancel = nil
	s.popupItems = nil
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	if fn != nil && int(index) < len(items) {
		go fn(items[index].Data)
	}
	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.HidePluginPopup(ctx, nil)
		fut.Struct() //nolint:errcheck
		rel()
	}
	_, err := call.AllocResults()
	return err
}

// PluginPopupCancelled implements proto.EditorService_Server.
func (s *editorService) PluginPopupCancelled(_ context.Context, call proto.EditorService_pluginPopupCancelled) error {
	s.mu.Lock()
	fn := s.popupOnCancel
	s.popupOnSelect = nil
	s.popupOnCancel = nil
	s.popupItems = nil
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	if fn != nil {
		go fn()
	}
	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.HidePluginPopup(ctx, nil)
		fut.Struct() //nolint:errcheck
		rel()
	}
	_, err := call.AllocResults()
	return err
}

// PluginInputConfirmed implements proto.EditorService_Server.
func (s *editorService) PluginInputConfirmed(_ context.Context, call proto.EditorService_pluginInputConfirmed) error {
	text, err := call.Args().Text()
	if err != nil {
		return err
	}
	s.mu.Lock()
	fn := s.inputOnConfirm
	s.inputOnConfirm = nil
	s.inputOnCancel = nil
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	if fn != nil {
		go fn(text)
	}
	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.HideInputPrompt(ctx, nil)
		fut.Struct() //nolint:errcheck
		rel()
	}
	_, err = call.AllocResults()
	return err
}

// PluginInputCancelled implements proto.EditorService_Server.
func (s *editorService) PluginInputCancelled(_ context.Context, call proto.EditorService_pluginInputCancelled) error {
	s.mu.Lock()
	fn := s.inputOnCancel
	s.inputOnConfirm = nil
	s.inputOnCancel = nil
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	if fn != nil {
		go fn()
	}
	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.HideInputPrompt(ctx, nil)
		fut.Struct() //nolint:errcheck
		rel()
	}
	_, err := call.AllocResults()
	return err
}

func (s *editorService) UpdateViewport(_ context.Context, call proto.EditorService_updateViewport) error {
	args := call.Args()
	clientID := args.ClientId()
	topLine := args.TopLine()
	height := args.Height()

	s.mu.Lock()
	if entry, ok := s.clientMap[clientID]; ok {
		entry.topLine = topLine
		entry.height = height
	}
	s.mu.Unlock()

	_, err := call.AllocResults()
	return err
}

func (s *editorService) GetPluginBindings(ctx context.Context, call proto.EditorService_getPluginBindings) error {
	waitCtx, cancel := context.WithTimeout(ctx, pluginReadyTimeout)
	s.pluginMgr.WaitReady(waitCtx)
	cancel()

	bindings := s.pluginMgr.AllPluginBindings()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewBindings(int32(len(bindings)))
	if err != nil {
		return err
	}
	for i, b := range bindings {
		item := list.At(i)
		if err := item.SetPluginName(b.PluginName); err != nil {
			return err
		}
		if err := item.SetKey(b.Key); err != nil {
			return err
		}
		if err := item.SetDescription(b.Description); err != nil {
			return err
		}
	}
	return nil
}

// setMenuItems recursively fills a MenuItemInfo_List from a []plugin.MenuItem tree.
func setMenuItems(list proto.MenuItemInfo_List, items []plugin.MenuItem) error {
	for i, it := range items {
		node := list.At(i)
		if err := node.SetLabel(it.Label); err != nil {
			return err
		}
		if err := node.SetKey(it.Key); err != nil {
			return err
		}
		if err := node.SetPluginName(it.PluginName); err != nil {
			return err
		}
		if err := node.SetCommand(it.Command); err != nil {
			return err
		}
		if len(it.Children) > 0 {
			childList, err := node.NewChildren(int32(len(it.Children)))
			if err != nil {
				return err
			}
			if err := setMenuItems(childList, it.Children); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *editorService) GetMenuItems(ctx context.Context, call proto.EditorService_getMenuItems) error {
	waitCtx, cancel := context.WithTimeout(ctx, pluginReadyTimeout)
	s.pluginMgr.WaitReady(waitCtx)
	cancel()

	items := s.pluginMgr.AllMenuItems()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewItems(int32(len(items)))
	if err != nil {
		return err
	}
	return setMenuItems(list, items)
}

func (s *editorService) InvokePluginMenuAction(ctx context.Context, call proto.EditorService_invokePluginMenuAction) error {
	args := call.Args()
	pluginName, err := args.PluginName()
	if err != nil {
		return err
	}
	command, err := args.Command()
	if err != nil {
		return err
	}
	clientID := args.ClientId()
	bufID := args.BufId()
	cursorLine := args.CursorLine()
	cursorCol := args.CursorCol()

	handled, edits, resLine, resCol, hasCursor, captureKeys, handleErr := s.pluginMgr.InvokeMenuAction(ctx, pluginName, command, bufID, clientID, cursorLine, cursorCol)
	if handleErr != nil {
		return handleErr
	}
	// Same as HandlePluginKey: plugins should call applyEdit explicitly rather
	// than relying on edits returned atomically here.
	_ = edits

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetHandled(handled)
	result.SetHasCursor(hasCursor)
	if hasCursor {
		result.SetCursorLine(resLine)
		result.SetCursorCol(resCol)
	}
	result.SetCaptureKeys(captureKeys)
	return nil
}

func (s *editorService) GetPluginKeys(_ context.Context, call proto.EditorService_getPluginKeys) error {
	keys := s.pluginMgr.AllRegisteredKeys()
	serverLog("GetPluginKeys called, returning %d keys: %v", len(keys), keys)
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewKeys(int32(len(keys)))
	if err != nil {
		return err
	}
	for i, k := range keys {
		if err := list.Set(i, k); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorService) GetPluginInsertChars(_ context.Context, call proto.EditorService_getPluginInsertChars) error {
	chars := s.pluginMgr.AllRegisteredInsertChars()
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewChars(int32(len(chars)))
	if err != nil {
		return err
	}
	for i, c := range chars {
		if err := list.Set(i, c); err != nil {
			return err
		}
	}
	return nil
}
