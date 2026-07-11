package plugin

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// editorApiServer is the server-side implementation of pluginproto.EditorApi.
// One instance is created per plugin and passed to plugin.initialize.
type editorApiServer struct {
	reg    *registeredPlugin
	bridge ServerBridge
}

// -- Registration methods --

func (s *editorApiServer) RegisterKeyBinding(_ context.Context, call pluginproto.EditorApi_registerKeyBinding) error {
	args := call.Args()
	trigger, err := args.Trigger()
	if err != nil {
		return err
	}
	handler := args.Handler()
	if !handler.IsValid() {
		return fmt.Errorf("invalid key handler")
	}
	s.reg.mu.Lock()
	if old, ok := s.reg.keyBindings[trigger]; ok {
		old.Release()
	}
	s.reg.keyBindings[trigger] = handler.AddRef()
	s.reg.mu.Unlock()
	if s.bridge != nil {
		s.bridge.PluginKeyRegistered(trigger)
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) RegisterInsertHook(_ context.Context, call pluginproto.EditorApi_registerInsertHook) error {
	args := call.Args()
	char, err := args.Char()
	if err != nil {
		return err
	}
	handler := args.Handler()
	if !handler.IsValid() {
		return fmt.Errorf("invalid key handler")
	}
	s.reg.mu.Lock()
	if old, ok := s.reg.insertHooks[char]; ok {
		old.Release()
	}
	s.reg.insertHooks[char] = handler.AddRef()
	s.reg.mu.Unlock()
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) RegisterCommand(_ context.Context, call pluginproto.EditorApi_registerCommand) error {
	args := call.Args()
	name, err := args.Name()
	if err != nil {
		return err
	}
	handler := args.Handler()
	if !handler.IsValid() {
		return fmt.Errorf("invalid command handler")
	}
	s.reg.mu.Lock()
	if old, ok := s.reg.commands[name]; ok {
		old.Release()
	}
	s.reg.commands[name] = handler.AddRef()
	s.reg.mu.Unlock()
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) RegisterBufferHandler(_ context.Context, call pluginproto.EditorApi_registerBufferHandler) error {
	handler := call.Args().Handler()
	if !handler.IsValid() {
		return fmt.Errorf("invalid buffer handler")
	}
	s.reg.mu.Lock()
	s.reg.bufHandler.Release()
	s.reg.bufHandler = handler.AddRef()
	h := s.reg.bufHandler
	s.reg.mu.Unlock()

	// Fire OnOpen for any buffers that were opened before this plugin started.
	if s.bridge != nil {
		refs := s.bridge.PluginOpenBuffers()
		for _, ref := range refs {
			bufID := ref.BufID
			path := ref.Path
			go func(h pluginproto.BufferEventHandler) {
				ctx := context.Background()
				fut, rel := h.OnOpen(ctx, func(ps pluginproto.BufferEventHandler_onOpen_Params) error {
					ev, err := ps.NewEvent()
					if err != nil {
						return err
					}
					ev.SetBufId(bufID)
					return ev.SetPath(path)
				})
				defer rel()
				fut.Struct() //nolint:errcheck
			}(h.AddRef())
		}
	}

	_, err := call.AllocResults()
	return err
}

func (s *editorApiServer) RegisterDecorations(_ context.Context, call pluginproto.EditorApi_registerDecorations) error {
	provider := call.Args().Provider()
	if !provider.IsValid() {
		return fmt.Errorf("invalid decoration provider")
	}
	s.reg.mu.Lock()
	s.reg.decorProvider.Release()
	s.reg.decorProvider = provider.AddRef()
	s.reg.mu.Unlock()
	_, err := call.AllocResults()
	return err
}

func (s *editorApiServer) RegisterActionProvider(_ context.Context, call pluginproto.EditorApi_registerActionProvider) error {
	provider := call.Args().Provider()
	if !provider.IsValid() {
		return fmt.Errorf("invalid action provider")
	}
	s.reg.mu.Lock()
	s.reg.actionProvider.Release()
	s.reg.actionProvider = provider.AddRef()
	s.reg.mu.Unlock()
	_, err := call.AllocResults()
	return err
}

// -- Editor effect methods --

func (s *editorApiServer) ApplyEdit(_ context.Context, call pluginproto.EditorApi_applyEdit) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	bufID := args.BufId()
	rawEdits, err := args.Edits()
	if err != nil {
		return err
	}
	edits := make([]TextEdit, rawEdits.Len())
	for i := range edits {
		item := rawEdits.At(i)
		from, err := item.From()
		if err != nil {
			return err
		}
		to, err := item.To()
		if err != nil {
			return err
		}
		text, err := item.NewText()
		if err != nil {
			return err
		}
		edits[i] = TextEdit{
			FromLine: from.Line(), FromCol: from.Col(),
			ToLine:   to.Line(), ToCol: to.Col(),
			NewText:  text,
		}
	}
	if err := s.bridge.PluginApplyEdit(bufID, edits); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) MoveCursor(_ context.Context, call pluginproto.EditorApi_moveCursor) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	pos, err := args.Pos()
	if err != nil {
		return err
	}
	if err := s.bridge.PluginMoveCursor(args.BufId(), pos.Line(), pos.Col()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) OpenFile(_ context.Context, call pluginproto.EditorApi_openFile) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	path, err := args.Path()
	if err != nil {
		return err
	}
	if err := s.bridge.PluginOpenFile(path, args.Line()); err != nil {
		return err
	}
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) ShowMessage(_ context.Context, call pluginproto.EditorApi_showMessage) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	text, err := call.Args().Text()
	if err != nil {
		return err
	}
	s.bridge.PluginShowMessage(text)
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) RunProcess(_ context.Context, call pluginproto.EditorApi_runProcess) error {
	args := call.Args()
	cmdStr, err := args.Cmd()
	if err != nil {
		return err
	}
	rawArgs, err := args.Args()
	if err != nil {
		return err
	}
	cmdArgs := make([]string, rawArgs.Len())
	for i := range cmdArgs {
		cmdArgs[i], err = rawArgs.At(i)
		if err != nil {
			return err
		}
	}

	var stdout, stderr string
	var exitCode int32
	if s.bridge != nil {
		stdout, stderr, exitCode, err = s.bridge.PluginRunProcess(cmdStr, cmdArgs)
	} else {
		cmd := exec.Command(cmdStr, cmdArgs...)
		outBytes, _ := cmd.Output()
		stdout = string(outBytes)
	}

	res, allocErr := call.AllocResults()
	if allocErr != nil {
		return allocErr
	}
	if setErr := res.SetStdout(stdout); setErr != nil {
		return setErr
	}
	if setErr := res.SetStderr(stderr); setErr != nil {
		return setErr
	}
	res.SetExitCode(exitCode)
	return err
}

// -- Document query methods --

func (s *editorApiServer) ReadBuffer(_ context.Context, call pluginproto.EditorApi_readBuffer) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	content, err := s.bridge.PluginReadBuffer(call.Args().BufId())
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	return res.SetContent(content)
}

func (s *editorApiServer) ReadLines(_ context.Context, call pluginproto.EditorApi_readLines) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	lines, err := s.bridge.PluginReadLines(args.BufId(), args.StartLine(), args.EndLine())
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewLines(int32(len(lines)))
	if err != nil {
		return err
	}
	for i, line := range lines {
		if err := list.Set(i, line); err != nil {
			return err
		}
	}
	return nil
}

func (s *editorApiServer) ReadRange(_ context.Context, call pluginproto.EditorApi_readRange) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	from, err := args.From()
	if err != nil {
		return err
	}
	to, err := args.To()
	if err != nil {
		return err
	}
	text, err := s.bridge.PluginReadRange(args.BufId(), from.Line(), from.Col(), to.Line(), to.Col())
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	return res.SetText(text)
}

func (s *editorApiServer) WordAt(_ context.Context, call pluginproto.EditorApi_wordAt) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	pos, err := args.Pos()
	if err != nil {
		return err
	}
	startLine, startCol, endLine, endCol, found, bridgeErr := s.bridge.PluginWordAt(args.BufId(), pos.Line(), pos.Col())
	if bridgeErr != nil {
		return bridgeErr
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetFound(found)
	if found {
		start, err := res.NewStart()
		if err != nil {
			return err
		}
		start.SetLine(startLine)
		start.SetCol(startCol)
		end, err := res.NewEnd()
		if err != nil {
			return err
		}
		end.SetLine(endLine)
		end.SetCol(endCol)
	}
	return nil
}

func (s *editorApiServer) BufferInfo(_ context.Context, call pluginproto.EditorApi_bufferInfo) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	path, langID, lineCount, isDirty, err := s.bridge.PluginBufferInfo(call.Args().BufId())
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if err := res.SetPath(path); err != nil {
		return err
	}
	if err := res.SetLanguageId(langID); err != nil {
		return err
	}
	res.SetLineCount(lineCount)
	res.SetIsDirty(isDirty)
	return nil
}

func (s *editorApiServer) VisibleRange(_ context.Context, call pluginproto.EditorApi_visibleRange) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	startLine, endLine, err := s.bridge.PluginVisibleRange(call.Args().ClientId())
	if err != nil {
		return err
	}
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetStartLine(startLine)
	res.SetEndLine(endLine)
	return nil
}

func (s *editorApiServer) SetBookmark(_ context.Context, call pluginproto.EditorApi_setBookmark) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	note, err := args.Note()
	if err != nil {
		return err
	}
	marker, err := args.Marker()
	if err != nil {
		return err
	}
	s.bridge.PluginSetBookmark(args.BufId(), args.Line(), args.Col(), note, marker)
	_, err = call.AllocResults()
	return err
}

func (s *editorApiServer) ShowBookmarks(_ context.Context, call pluginproto.EditorApi_showBookmarks) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	s.bridge.PluginShowBookmarks()
	_, err := call.AllocResults()
	return err
}

func (s *editorApiServer) PromptBookmark(_ context.Context, call pluginproto.EditorApi_promptBookmark) error {
	if s.bridge == nil {
		return fmt.Errorf("no server bridge")
	}
	args := call.Args()
	marker, err := args.Marker()
	if err != nil {
		return err
	}
	s.bridge.PluginPromptBookmark(args.BufId(), args.Line(), args.Col(), marker)
	_, err = call.AllocResults()
	return err
}
