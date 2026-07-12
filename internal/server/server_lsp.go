package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lsp"
	proto "github.com/indiejames/indigo/internal/proto"
)

func (s *editorService) GetDiagnostics(_ context.Context, call proto.EditorService_getDiagnostics) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	diags := s.lspMgr.GetDiagnostics(path)
	ready := s.lspMgr.HasClient(path)

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetLspReady(ready)
	if len(diags) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(diags)))
	if err != nil {
		return err
	}
	for i, d := range diags {
		item := list.At(i)
		item.SetLine(uint32(d.Range.Start.Line))
		item.SetCol(uint32(d.Range.Start.Character))
		item.SetEndLine(uint32(d.Range.End.Line))
		item.SetEndCol(uint32(d.Range.End.Character))
		item.SetSeverity(uint8(d.Severity))
		item.SetMessage_(d.Message) //nolint:errcheck
		item.SetSource(d.Source)    //nolint:errcheck
	}
	return nil
}

func (s *editorService) Hover(_ context.Context, call proto.EditorService_hover) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	h, err := s.lspMgr.Hover(path, line, col)
	if err != nil {
		return err
	}
	if h == nil {
		return nil
	}
	text := h.Text()
	if text == "" {
		return nil
	}
	result.SetFound(true)
	result.SetContents(text) //nolint:errcheck
	return nil
}

func (s *editorService) SignatureHelp(_ context.Context, call proto.EditorService_signatureHelp) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	sh, err := s.lspMgr.SignatureHelp(path, line, col)
	if err != nil || sh == nil || len(sh.Signatures) == 0 {
		return nil
	}
	result.SetFound(true)
	result.SetActiveSignature(uint32(sh.ActiveSignature))
	result.SetActiveParameter(uint32(sh.ActiveParameter))
	sigs, err := result.NewSignatures(int32(len(sh.Signatures)))
	if err != nil {
		return nil
	}
	for i, sig := range sh.Signatures {
		s := sigs.At(i)
		s.SetLabel(sig.Label)                 //nolint:errcheck
		s.SetDocumentation(sig.Documentation) //nolint:errcheck
		if len(sig.Parameters) > 0 {
			params, err := s.NewParameters(int32(len(sig.Parameters)))
			if err != nil {
				continue
			}
			for j, p := range sig.Parameters {
				params.At(j).SetLabel(p.Label) //nolint:errcheck
			}
		}
	}
	return nil
}

func (s *editorService) Complete(_ context.Context, call proto.EditorService_complete) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	items, err := s.lspMgr.Complete(path, line, col)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(items) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(items)))
	if err != nil {
		return err
	}
	for i, it := range items {
		ci := list.At(i)
		ci.SetLabel(it.Label) //nolint:errcheck
		ci.SetKind(uint8(it.Kind))
		ci.SetDetail(it.Detail)         //nolint:errcheck
		ci.SetInsertText(it.InsertText) //nolint:errcheck
	}
	return nil
}

func (s *editorService) Definition(_ context.Context, call proto.EditorService_definition) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	locs, err := s.lspMgr.Definition(path, line, col)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(locs) == 0 {
		return nil
	}
	loc := locs[0]
	result, err := res.NewResult()
	if err != nil {
		return err
	}
	result.SetFound(true)
	result.SetPath(lsp.URIToPath(loc.URI)) //nolint:errcheck
	result.SetLine(uint32(loc.Range.Start.Line))
	result.SetCol(uint32(loc.Range.Start.Character))
	return nil
}

func (s *editorService) References(_ context.Context, call proto.EditorService_references) error {
	args := call.Args()
	bufID := args.BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	locs, err := s.lspMgr.References(path, int(args.Line()), int(args.Col()))
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(locs) == 0 {
		return nil
	}

	s.mu.Lock()
	buf := s.buffers[bufID].buf
	s.mu.Unlock()

	list, err := res.NewLocations(int32(len(locs)))
	if err != nil {
		return err
	}
	for i, loc := range locs {
		fl := list.At(i)
		locPath := lsp.URIToPath(loc.URI)
		fl.SetPath(locPath)  //nolint:errcheck
		fl.SetLine(uint32(loc.Range.Start.Line))
		fl.SetCol(uint32(loc.Range.Start.Character))
		// Preview: if it's the same file, read from buffer; otherwise leave empty.
		if locPath == path && loc.Range.Start.Line < buf.LineCount() {
			fl.SetPreview(buf.Line(loc.Range.Start.Line)) //nolint:errcheck
		}
	}
	return nil
}

func (s *editorService) WorkspaceSymbols(_ context.Context, call proto.EditorService_workspaceSymbols) error {
	args := call.Args()
	bufID := args.BufId()
	query, err := args.Query()
	if err != nil {
		return err
	}

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	syms, err := s.lspMgr.WorkspaceSymbols(path, query)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(syms) == 0 {
		return nil
	}

	list, err := res.NewSymbols(int32(len(syms)))
	if err != nil {
		return err
	}
	for i, sym := range syms {
		sr := list.At(i)
		sr.SetName(sym.Name)                             //nolint:errcheck
		sr.SetKind(uint8(sym.Kind))
		sr.SetContainerName(sym.ContainerName)           //nolint:errcheck
		sr.SetPath(lsp.URIToPath(sym.Location.URI))      //nolint:errcheck
		sr.SetLine(uint32(sym.Location.Range.Start.Line))
		sr.SetCol(uint32(sym.Location.Range.Start.Character))
	}
	return nil
}

func (s *editorService) DocumentSymbols(_ context.Context, call proto.EditorService_documentSymbols) error {
	args := call.Args()
	bufID := args.BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	s.mu.Unlock()

	syms, err := s.lspMgr.DocumentSymbols(path)
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(syms) == 0 {
		return nil
	}

	list, err := res.NewSymbols(int32(len(syms)))
	if err != nil {
		return err
	}
	for i, sym := range syms {
		sr := list.At(i)
		sr.SetName(sym.Name)                             //nolint:errcheck
		sr.SetKind(uint8(sym.Kind))
		sr.SetContainerName(sym.ContainerName)           //nolint:errcheck
		sr.SetPath(lsp.URIToPath(sym.Location.URI))      //nolint:errcheck
		sr.SetLine(uint32(sym.Location.Range.Start.Line))
		sr.SetCol(uint32(sym.Location.Range.Start.Character))
	}
	return nil
}

func (s *editorService) Format(_ context.Context, call proto.EditorService_format) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	content := entry.buf.Content()
	s.mu.Unlock()

	formatted, changed, fmtErr := s.fmtMgr.Format(path, content)

	noFormatter := errors.Is(fmtErr, format.ErrNoFormatter)
	if fmtErr != nil && !noFormatter {
		return fmtErr
	}

	if changed {
		newBuf := document.New(path, formatted)
		newBuf.MarkDirty()
		s.mu.Lock()
		entry.buf = newBuf
		s.buffers[bufID] = entry
		s.mu.Unlock()
		go s.lspMgr.DidChange(path, formatted)
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if changed {
		if err := res.SetContent(formatted); err != nil {
			return err
		}
	}
	res.SetChanged(changed)
	res.SetNoFormatter(noFormatter)
	return nil
}
