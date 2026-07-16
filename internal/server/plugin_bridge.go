package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

func serverLog(format string, args ...any) {
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	fmt.Fprintf(f, "[server] "+format+"\n", args...) //nolint:errcheck
}

// pluginClientID is the client ID used for edits applied by plugins.
const pluginClientID uint64 = 0

// PluginReadBuffer implements plugin.ServerBridge.
func (s *editorService) PluginReadBuffer(bufID uint32) (string, error) {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown buffer %d", bufID)
	}
	return entry.buf.Content(), nil
}

// PluginReadLines implements plugin.ServerBridge.
func (s *editorService) PluginReadLines(bufID uint32, start, end uint32) ([]string, error) {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown buffer %d", bufID)
	}
	count := uint32(entry.buf.LineCount())
	if start >= count {
		return nil, nil
	}
	if end > count {
		end = count
	}
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		lines = append(lines, entry.buf.Line(int(i)))
	}
	return lines, nil
}

// PluginReadRange implements plugin.ServerBridge.
func (s *editorService) PluginReadRange(bufID uint32, fromLine, fromCol, toLine, toCol uint32) (string, error) {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown buffer %d", bufID)
	}
	buf := entry.buf
	content := []rune(buf.Content())
	fromOff := buf.RuneOffset(int(fromLine), int(fromCol))
	toOff := buf.RuneOffset(int(toLine), int(toCol))
	if fromOff < 0 {
		fromOff = 0
	}
	if toOff > len(content) {
		toOff = len(content)
	}
	if fromOff >= toOff {
		return "", nil
	}
	return string(content[fromOff:toOff]), nil
}

// PluginWordAt implements plugin.ServerBridge.
func (s *editorService) PluginWordAt(bufID uint32, line, col uint32) (startLine, startCol, endLine, endCol uint32, found bool, err error) {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return 0, 0, 0, 0, false, fmt.Errorf("unknown buffer %d", bufID)
	}
	lineStr := entry.buf.Line(int(line))
	runes := []rune(lineStr)
	n := len(runes)
	c := int(col)
	if n == 0 || c >= n || !isWordRune(runes[c]) {
		return 0, 0, 0, 0, false, nil
	}
	start := c
	for start > 0 && isWordRune(runes[start-1]) {
		start--
	}
	end := c
	for end+1 < n && isWordRune(runes[end+1]) {
		end++
	}
	return line, uint32(start), line, uint32(end), true, nil
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// PluginBufferInfo implements plugin.ServerBridge.
func (s *editorService) PluginBufferInfo(bufID uint32) (path, langID string, lineCount uint32, isDirty bool, err error) {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return "", "", 0, false, fmt.Errorf("unknown buffer %d", bufID)
	}
	path = entry.buf.Path()
	langID = langIDForPath(path)
	lineCount = uint32(entry.buf.LineCount())
	isDirty = entry.buf.Dirty()
	return path, langID, lineCount, isDirty, nil
}

// langIDForPath returns a language ID string based on file extension.
func langIDForPath(path string) string {
	dot := strings.LastIndexByte(path, '.')
	if dot < 0 {
		return ""
	}
	switch path[dot+1:] {
	case "go":
		return "go"
	case "rs":
		return "rust"
	case "ts":
		return "typescript"
	case "tsx":
		return "typescriptreact"
	case "js":
		return "javascript"
	case "jsx":
		return "javascriptreact"
	case "py":
		return "python"
	case "c":
		return "c"
	case "cpp", "cc", "cxx":
		return "cpp"
	case "h", "hpp":
		return "cpp"
	case "java":
		return "java"
	case "rb":
		return "ruby"
	case "lua":
		return "lua"
	case "zig":
		return "zig"
	case "md":
		return "markdown"
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "toml":
		return "toml"
	case "sh", "bash":
		return "shellscript"
	default:
		return ""
	}
}

// PluginVisibleRange implements plugin.ServerBridge.
func (s *editorService) PluginVisibleRange(clientID uint64) (startLine, endLine uint32, err error) {
	s.mu.Lock()
	entry, ok := s.clientMap[clientID]
	s.mu.Unlock()
	if !ok {
		return 0, 0, fmt.Errorf("unknown client %d", clientID)
	}
	end := entry.topLine + entry.height
	if end > 0 {
		end--
	}
	return entry.topLine, end, nil
}

// PluginKeyRegistered implements plugin.ServerBridge.
// Notifies all connected clients that a plugin registered a key binding.
func (s *editorService) PluginKeyRegistered(trigger string) {
	s.mu.Lock()
	mapLen := len(s.clientMap)
	for id, ce := range s.clientMap {
		serverLog("PluginKeyRegistered: clientMap[%d].IsValid=%v", id, ce.callback.IsValid())
	}
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	serverLog("PluginKeyRegistered: trigger=%q, clientMapLen=%d, clients=%d", trigger, mapLen, len(callbacks))

	ctx := context.Background()
	for i, cb := range callbacks {
		fut, rel := cb.KeyRegistered(ctx, func(p proto.ClientCallback_keyRegistered_Params) error {
			return p.SetTrigger(trigger)
		})
		_, err := fut.Struct()
		rel()
		serverLog("PluginKeyRegistered: KeyRegistered to client %d: err=%v", i, err)
	}
}

// PluginApplyEdit implements plugin.ServerBridge.
// Applies a sequence of text edits to a buffer. Edits are applied as ops with
// a reserved plugin client ID.
func (s *editorService) PluginApplyEdit(bufID uint32, edits []plugin.TextEdit) error {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown buffer %d", bufID)
	}

	for _, e := range edits {
		if e.FromLine == e.ToLine && e.FromCol == e.ToCol {
			// Pure insert.
			entry.buf.Apply(document.Op{
				ClientID:   pluginClientID,
				Type:       document.OpInsert,
				InsertLine: int(e.FromLine),
				InsertCol:  int(e.FromCol),
				InsertText: e.NewText,
			})
		} else if e.NewText == "" {
			// Pure delete.
			entry.buf.Apply(document.Op{
				ClientID: pluginClientID,
				Type:     document.OpDelete,
				FromLine: int(e.FromLine),
				FromCol:  int(e.FromCol),
				ToLine:   int(e.ToLine),
				ToCol:    int(e.ToCol),
			})
		} else {
			// Replace: delete then insert at the same position.
			entry.buf.Apply(document.Op{
				ClientID: pluginClientID,
				Type:     document.OpDelete,
				FromLine: int(e.FromLine),
				FromCol:  int(e.FromCol),
				ToLine:   int(e.ToLine),
				ToCol:    int(e.ToCol),
			})
			entry.buf.Apply(document.Op{
				ClientID:   pluginClientID,
				Type:       document.OpInsert,
				InsertLine: int(e.FromLine),
				InsertCol:  int(e.FromCol),
				InsertText: e.NewText,
			})
		}
	}
	return nil
}

// PluginMoveCursor implements plugin.ServerBridge.
// Pushes a cursor move to all clients that have bufID open.
func (s *editorService) PluginMoveCursor(bufID uint32, line, col uint32) error {
	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	callbacks := s.callbacksForBuffer(entry)
	s.mu.Unlock()

	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.MoveCursor(ctx, func(p proto.ClientCallback_moveCursor_Params) error {
			p.SetBufId(bufID)
			p.SetLine(line)
			p.SetCol(col)
			return nil
		})
		fut.Struct() //nolint:errcheck
		rel()
	}
	return nil
}

// PluginOpenFile implements plugin.ServerBridge.
// Pushes an open-file command to all connected clients.
func (s *editorService) PluginOpenFile(path string, line uint32) error {
	s.mu.Lock()
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.OpenFile(ctx, func(p proto.ClientCallback_openFile_Params) error {
			p.SetLine(line)
			return p.SetPath(path)
		})
		fut.Struct() //nolint:errcheck
		rel()
	}
	return nil
}

// PluginShowMessage implements plugin.ServerBridge.
// Pushes a status message to one client (clientID != 0) or to every
// connected client (clientID == 0).
func (s *editorService) PluginShowMessage(clientID uint64, text string) {
	s.mu.Lock()
	var callbacks []proto.ClientCallback
	if clientID != 0 {
		if ce, ok := s.clientMap[clientID]; ok && ce.callback.IsValid() {
			callbacks = []proto.ClientCallback{ce.callback}
		}
	} else {
		callbacks = s.allCallbacks()
	}
	s.mu.Unlock()

	serverLog("PluginShowMessage: clientID=%d text=%q, targets=%d", clientID, text, len(callbacks))

	ctx := context.Background()
	for i, cb := range callbacks {
		fut, rel := cb.ShowMessage(ctx, func(p proto.ClientCallback_showMessage_Params) error {
			return p.SetText(text)
		})
		_, err := fut.Struct()
		rel()
		serverLog("PluginShowMessage: ShowMessage to target %d: err=%v", i, err)
	}
}

// callbacksForBuffer returns the callbacks for all clients that have bufID open.
// Must be called with s.mu held.
func (s *editorService) callbacksForBuffer(entry *bufferEntry) []proto.ClientCallback {
	var out []proto.ClientCallback
	for clientID := range entry.clients {
		if ce, ok := s.clientMap[clientID]; ok && ce.callback.IsValid() {
			out = append(out, ce.callback)
		}
	}
	return out
}

// allCallbacks returns the callbacks for every connected client.
// Must be called with s.mu held.
func (s *editorService) allCallbacks() []proto.ClientCallback {
	out := make([]proto.ClientCallback, 0, len(s.clientMap))
	for _, ce := range s.clientMap {
		if ce.callback.IsValid() {
			out = append(out, ce.callback)
		}
	}
	return out
}

// PluginOpenBuffers implements plugin.ServerBridge.
func (s *editorService) PluginOpenBuffers() []plugin.PluginBufferRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := make([]plugin.PluginBufferRef, 0, len(s.buffers))
	for id, entry := range s.buffers {
		refs = append(refs, plugin.PluginBufferRef{BufID: id, Path: entry.buf.Path()})
	}
	return refs
}

// PluginShowPopup implements plugin.ServerBridge.
// Stores the selection callbacks and pushes showPluginPopup to all clients.
func (s *editorService) PluginShowPopup(title string, items []plugin.PluginPopupItem, onSelect func(data string), onCancel func()) {
	s.mu.Lock()
	s.popupOnSelect = onSelect
	s.popupOnCancel = onCancel
	s.popupItems = items
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.ShowPluginPopup(ctx, func(p proto.ClientCallback_showPluginPopup_Params) error {
			if err := p.SetTitle(title); err != nil {
				return err
			}
			list, err := p.NewItems(int32(len(items)))
			if err != nil {
				return err
			}
			for i, item := range items {
				pi := list.At(i)
				if err := pi.SetLabel(item.Label); err != nil {
					return err
				}
				if err := pi.SetSublabel(item.Sublabel); err != nil {
					return err
				}
				if err := pi.SetData(item.Data); err != nil {
					return err
				}
			}
			return nil
		})
		fut.Struct() //nolint:errcheck
		rel()
	}
}

// PluginShowInputPrompt implements plugin.ServerBridge.
// Stores the confirm/cancel callbacks and pushes showInputPrompt to all clients.
func (s *editorService) PluginShowInputPrompt(title, placeholder string, onConfirm func(text string), onCancel func()) {
	s.mu.Lock()
	s.inputOnConfirm = onConfirm
	s.inputOnCancel = onCancel
	callbacks := s.allCallbacks()
	s.mu.Unlock()

	ctx := context.Background()
	for _, cb := range callbacks {
		fut, rel := cb.ShowInputPrompt(ctx, func(p proto.ClientCallback_showInputPrompt_Params) error {
			if err := p.SetTitle(title); err != nil {
				return err
			}
			return p.SetPlaceholder(placeholder)
		})
		fut.Struct() //nolint:errcheck
		rel()
	}
}

// PluginRunProcess implements plugin.ServerBridge.
func (s *editorService) PluginRunProcess(cmdStr string, args []string) (stdout, stderr string, exitCode int32, err error) {
	cmd := exec.Command(cmdStr, args...)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		} else {
			err = runErr
		}
	}
	return stdout, stderr, exitCode, err
}
