package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// currentPluginDiags returns entry's plugin diagnostics that are still
// valid against its buffer's current version — see pluginDiagEntry's doc
// comment on why entries computed against an older version are excluded
// rather than returned stale. Caller must hold s.mu.
func currentPluginDiags(entry *bufferEntry) []lsp.Diagnostic {
	curVersion := entry.buf.Version()
	var out []lsp.Diagnostic
	for _, e := range entry.pluginDiags {
		if e.version != curVersion {
			continue // left behind by edits since publish; would point at the wrong text
		}
		out = append(out, e.diags...)
	}
	return out
}

// mergedDiagnostics combines LSP + lint + (already version-filtered) plugin
// diagnostics for one path, the same three sources GetDiagnostics and
// GetWorkspaceDiagnostics both draw from.
func (s *editorService) mergedDiagnostics(path string, pluginDiags []lsp.Diagnostic) []lsp.Diagnostic {
	diags := append(s.lspMgr.GetDiagnostics(path), s.lintMgr.GetDiagnostics(path)...)
	return append(diags, pluginDiags...)
}

func (s *editorService) GetDiagnostics(_ context.Context, call proto.EditorService_getDiagnostics) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	pluginDiags := currentPluginDiags(entry)
	s.mu.Unlock()

	diags := s.mergedDiagnostics(path, pluginDiags)
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

// maxWorkspaceDiagnostics caps GetWorkspaceDiagnostics's result the same way
// maxGrepResults caps workspace search (internal/app/grep.go) — a
// misconfigured linter or a huge number of open buffers shouldn't produce
// an unbounded capnp payload.
const maxWorkspaceDiagnostics = 500

// pathBufferSnapshot is one open buffer's path and (already version-filtered)
// plugin diagnostics, captured under s.mu so GetWorkspaceDiagnostics(Summary)
// can iterate lspMgr/lintMgr — which have their own locking — without
// holding s.mu the whole time.
type pathBufferSnapshot struct {
	path        string
	pluginDiags []lsp.Diagnostic
}

func (s *editorService) snapshotOpenBuffers() []pathBufferSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snaps := make([]pathBufferSnapshot, 0, len(s.buffers))
	for _, entry := range s.buffers {
		snaps = append(snaps, pathBufferSnapshot{path: entry.buf.Path(), pluginDiags: currentPluginDiags(entry)})
	}
	return snaps
}

// GetWorkspaceDiagnostics aggregates diagnostics across every open buffer —
// see its doc comment in editor.capnp for the "open buffers only, not the
// whole workspace on disk" scope for now.
func (s *editorService) GetWorkspaceDiagnostics(_ context.Context, call proto.EditorService_getWorkspaceDiagnostics) error {
	snaps := s.snapshotOpenBuffers()

	type pathDiag struct {
		path string
		d    lsp.Diagnostic
	}
	var items []pathDiag
	truncated := false
outer:
	for _, sn := range snaps {
		for _, d := range s.mergedDiagnostics(sn.path, sn.pluginDiags) {
			if len(items) >= maxWorkspaceDiagnostics {
				truncated = true
				break outer
			}
			items = append(items, pathDiag{path: sn.path, d: d})
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetTruncated(truncated)
	if len(items) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(items)))
	if err != nil {
		return err
	}
	for i, it := range items {
		item := list.At(i)
		if err := item.SetPath(it.path); err != nil {
			return err
		}
		item.SetLine(uint32(it.d.Range.Start.Line))
		item.SetCol(uint32(it.d.Range.Start.Character))
		item.SetEndLine(uint32(it.d.Range.End.Line))
		item.SetEndCol(uint32(it.d.Range.End.Character))
		item.SetSeverity(uint8(it.d.Severity))
		item.SetMessage_(it.d.Message) //nolint:errcheck
		item.SetSource(it.d.Source)    //nolint:errcheck
	}
	return nil
}

// GetWorkspaceDiagnosticsSummary is the cheap counts-only counterpart to
// GetWorkspaceDiagnostics.
func (s *editorService) GetWorkspaceDiagnosticsSummary(_ context.Context, call proto.EditorService_getWorkspaceDiagnosticsSummary) error {
	snaps := s.snapshotOpenBuffers()

	var errCnt, warnCnt, infoCnt, fileCnt uint32
	for _, sn := range snaps {
		diags := s.mergedDiagnostics(sn.path, sn.pluginDiags)
		if len(diags) == 0 {
			continue
		}
		fileCnt++
		for _, d := range diags {
			switch d.Severity {
			case lsp.SeverityError:
				errCnt++
			case lsp.SeverityWarning:
				warnCnt++
			default:
				infoCnt++
			}
		}
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	res.SetErrorCount(errCnt)
	res.SetWarningCount(warnCnt)
	res.SetInfoCount(infoCnt)
	res.SetFileCount(fileCnt)
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

func (s *editorService) Complete(ctx context.Context, call proto.EditorService_complete) error {
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
	if err != nil {
		items = nil
	}
	pluginItems := s.pluginMgr.GetCompletions(ctx, bufID, uint32(line), uint32(col))

	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if len(items) == 0 && len(pluginItems) == 0 {
		return nil
	}
	list, err := res.NewItems(int32(len(items) + len(pluginItems)))
	if err != nil {
		return err
	}
	for i, it := range items {
		if err := writeCompletionItem(list.At(i), it); err != nil {
			return err
		}
	}
	for i, it := range pluginItems {
		if err := writePluginCompletionItem(list.At(len(items)+i), it); err != nil {
			return err
		}
	}
	return nil
}

// ResolveCompletion resolves a single completion item (the one the client is
// about to insert), filling in its additionalTextEdits — the auto-import line
// for a symbol from another module. The item's opaque data token, round-tripped
// from the earlier Complete response, tells the language server which candidate
// to resolve. On failure the item is returned unchanged so the client can still
// apply the primary insert.
func (s *editorService) ResolveCompletion(ctx context.Context, call proto.EditorService_resolveCompletion) error {
	args := call.Args()
	bufID := args.BufId()

	protoItem, err := args.Item()
	if err != nil {
		return err
	}

	source, err := protoItem.Source()
	if err != nil {
		return err
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	out, err := res.NewItem()
	if err != nil {
		return err
	}

	if source != "" {
		item := pluginCompletionFromProto(protoItem, source)
		resolved := s.pluginMgr.ResolveCompletion(ctx, source, item)
		return writePluginCompletionItem(out, resolved)
	}

	item, err := readCompletionItem(protoItem)
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

	resolved, rerr := s.lspMgr.ResolveCompletion(path, item)
	if rerr != nil {
		resolved = item
	}

	return writeCompletionItem(out, resolved)
}

// writeCompletionItem serializes an lsp.CompletionItem into its proto form,
// including the opaque resolve token (data) and any additionalTextEdits, which
// are populated only after ResolveCompletion and empty in Complete results.
func writeCompletionItem(dst proto.CompletionItem, src lsp.CompletionItem) error {
	if err := dst.SetLabel(src.Label); err != nil {
		return err
	}
	dst.SetKind(uint8(src.Kind))
	if err := dst.SetDetail(src.Detail); err != nil {
		return err
	}
	if err := dst.SetInsertText(src.InsertText); err != nil {
		return err
	}
	if err := dst.SetSortText(src.SortText); err != nil {
		return err
	}
	if err := dst.SetFilterText(src.FilterText); err != nil {
		return err
	}
	if src.TextEdit != nil {
		te, err := dst.NewTextEdit()
		if err != nil {
			return err
		}
		te.SetFromLine(uint32(src.TextEdit.Range.Start.Line))
		te.SetFromCol(uint32(src.TextEdit.Range.Start.Character))
		te.SetToLine(uint32(src.TextEdit.Range.End.Line))
		te.SetToCol(uint32(src.TextEdit.Range.End.Character))
		if err := te.SetNewText(src.TextEdit.NewText); err != nil {
			return err
		}
	}
	if len(src.Data) > 0 {
		if err := dst.SetData(src.Data); err != nil {
			return err
		}
	}
	if len(src.AdditionalTextEdits) > 0 {
		el, err := dst.NewAdditionalTextEdits(int32(len(src.AdditionalTextEdits)))
		if err != nil {
			return err
		}
		for j, e := range src.AdditionalTextEdits {
			ev := el.At(j)
			ev.SetFromLine(uint32(e.Range.Start.Line))
			ev.SetFromCol(uint32(e.Range.Start.Character))
			ev.SetToLine(uint32(e.Range.End.Line))
			ev.SetToCol(uint32(e.Range.End.Character))
			if err := ev.SetNewText(e.NewText); err != nil {
				return err
			}
		}
	}
	return nil
}

// readCompletionItem reconstructs the subset of an lsp.CompletionItem needed to
// resolve it: the display fields plus the opaque data token the language server
// keys resolution off. The data bytes are copied out of the capnp message so
// they stay valid once it's released.
func readCompletionItem(src proto.CompletionItem) (lsp.CompletionItem, error) {
	label, err := src.Label()
	if err != nil {
		return lsp.CompletionItem{}, err
	}
	detail, err := src.Detail()
	if err != nil {
		return lsp.CompletionItem{}, err
	}
	insert, err := src.InsertText()
	if err != nil {
		return lsp.CompletionItem{}, err
	}
	data, err := src.Data()
	if err != nil {
		return lsp.CompletionItem{}, err
	}
	item := lsp.CompletionItem{
		Label:      label,
		Kind:       lsp.CompletionItemKind(src.Kind()),
		Detail:     detail,
		InsertText: insert,
	}
	if len(data) > 0 {
		item.Data = append(json.RawMessage(nil), data...)
	}
	return item, nil
}

// writePluginCompletionItem serializes a plugin.PluginCompletion (from a
// plugin's CompletionProvider) into its proto form, tagging Source with the
// owning plugin's name so a later resolveCompletion call knows to route to
// the plugin manager instead of the language server.
func writePluginCompletionItem(dst proto.CompletionItem, src plugin.PluginCompletion) error {
	if err := dst.SetLabel(src.Label); err != nil {
		return err
	}
	dst.SetKind(src.Kind)
	if err := dst.SetDetail(src.Detail); err != nil {
		return err
	}
	if err := dst.SetInsertText(src.InsertText); err != nil {
		return err
	}
	if err := dst.SetSortText(src.SortText); err != nil {
		return err
	}
	if err := dst.SetFilterText(src.FilterText); err != nil {
		return err
	}
	if err := dst.SetSource(src.PluginName); err != nil {
		return err
	}
	if src.Data != "" {
		if err := dst.SetData([]byte(src.Data)); err != nil {
			return err
		}
	}
	if src.TextEdit != nil {
		te, err := dst.NewTextEdit()
		if err != nil {
			return err
		}
		te.SetFromLine(src.TextEdit.FromLine)
		te.SetFromCol(src.TextEdit.FromCol)
		te.SetToLine(src.TextEdit.ToLine)
		te.SetToCol(src.TextEdit.ToCol)
		if err := te.SetNewText(src.TextEdit.NewText); err != nil {
			return err
		}
	}
	return nil
}

// pluginCompletionFromProto reconstructs a plugin.PluginCompletion from a proto
// CompletionItem known to have come from (or be destined for) the named plugin,
// for the resolveCompletion round trip.
func pluginCompletionFromProto(src proto.CompletionItem, pluginName string) plugin.PluginCompletion {
	label, _ := src.Label()
	detail, _ := src.Detail()
	insert, _ := src.InsertText()
	sortText, _ := src.SortText()
	filterText, _ := src.FilterText()
	data, _ := src.Data()
	c := plugin.PluginCompletion{
		Label: label, Kind: src.Kind(), Detail: detail, InsertText: insert,
		SortText: sortText, FilterText: filterText, Data: string(data),
		PluginName: pluginName,
	}
	if src.HasTextEdit() {
		if te, err := src.TextEdit(); err == nil {
			newText, _ := te.NewText()
			c.TextEdit = &plugin.TextEdit{
				FromLine: te.FromLine(), FromCol: te.FromCol(),
				ToLine: te.ToLine(), ToCol: te.ToCol(),
				NewText: newText,
			}
		}
	}
	return c
}

// InlayHints returns inlay hints (inferred types, parameter names) for
// [startLine,endLine) — normally the client's visible viewport, not the whole
// file, since servers can be slow on large files and hints outside the
// viewport aren't rendered anyway.
func (s *editorService) InlayHints(_ context.Context, call proto.EditorService_inlayHints) error {
	args := call.Args()
	bufID := args.BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	buf := entry.buf
	s.mu.Unlock()

	hints, err := s.lspMgr.InlayHints(path,
		int(args.StartLine()), int(args.StartCol()), int(args.EndLine()), int(args.EndCol()))
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(hints) == 0 {
		return nil
	}

	list, err := res.NewHints(int32(len(hints)))
	if err != nil {
		return err
	}
	for i, h := range hints {
		hi := list.At(i)
		hi.SetLine(uint32(h.Position.Line))
		// h.Position.Character is a UTF-16 code-unit offset per the LSP spec's
		// default (indigo never negotiates an alternate positionEncoding), but
		// the client treats every column as a rune index — convert here, where
		// the buffer's actual line content is available, so nothing downstream
		// has to know about the distinction.
		col := h.Position.Character
		if h.Position.Line >= 0 && h.Position.Line < buf.LineCount() {
			col = utf16ColToRune([]rune(buf.Line(h.Position.Line)), col)
		}
		hi.SetCol(uint32(col))
		if err := hi.SetLabel(h.Text()); err != nil {
			return err
		}
		hi.SetKind(uint8(h.Kind))
		hi.SetPaddingLeft(h.PaddingLeft)
		hi.SetPaddingRight(h.PaddingRight)
	}
	return nil
}

// SemanticTokensRange returns LSP-derived syntax-coloring tokens for
// [startLine,endLine) — normally the client's visible viewport, not the whole
// file, since servers can be slow on large files and tokens outside the
// viewport aren't rendered anyway.
func (s *editorService) SemanticTokensRange(_ context.Context, call proto.EditorService_semanticTokensRange) error {
	args := call.Args()
	bufID := args.BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	buf := entry.buf
	s.mu.Unlock()

	tokens, err := s.lspMgr.SemanticTokensRange(path,
		int(args.StartLine()), int(args.StartCol()), int(args.EndLine()), int(args.EndCol()))
	res, rerr := call.AllocResults()
	if rerr != nil {
		return rerr
	}
	if err != nil || len(tokens) == 0 {
		return nil
	}

	list, err := res.NewTokens(int32(len(tokens)))
	if err != nil {
		return err
	}
	for i, tok := range tokens {
		ti := list.At(i)
		ti.SetLine(uint32(tok.Line))
		// tok.StartChar/Length are UTF-16 code-unit offsets per the LSP
		// spec's default (indigo never negotiates an alternate
		// positionEncoding), but the client treats every column/length as
		// rune counts — convert both the start and end offsets here, where
		// the buffer's actual line content is available, so a non-BMP rune
		// anywhere from the line start through the end of the token (not
		// just before it) is accounted for.
		startCol, length := tok.StartChar, tok.Length
		if tok.Line >= 0 && tok.Line < buf.LineCount() {
			lineRunes := []rune(buf.Line(tok.Line))
			runeStart := utf16ColToRune(lineRunes, tok.StartChar)
			runeEnd := utf16ColToRune(lineRunes, tok.StartChar+tok.Length)
			startCol, length = runeStart, runeEnd-runeStart
		}
		ti.SetCol(uint32(startCol))
		ti.SetLength(uint32(length))
		if err := ti.SetTokenType(tok.TokenType); err != nil {
			return err
		}
		if len(tok.Modifiers) > 0 {
			ml, err := ti.NewModifiers(int32(len(tok.Modifiers)))
			if err != nil {
				return err
			}
			for j, m := range tok.Modifiers {
				if err := ml.Set(j, m); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// utf16ColToRune converts col, a UTF-16 code-unit offset as used by LSP
// Position.character (indigo does not negotiate an alternate
// positionEncoding, so per spec every server defaults to UTF-16), into a rune
// index within line. Only runes outside the Basic Multilingual Plane
// (U+10000 and up — most emoji, some historic/rare scripts) differ: they
// encode as two UTF-16 code units but remain a single rune, so treating col
// directly as a rune index drifts by one for each such rune before the
// target position on the line.
func utf16ColToRune(line []rune, col int) int {
	units := 0
	for i, r := range line {
		if units >= col {
			return i
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return len(line)
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
		fl.SetPath(locPath) //nolint:errcheck
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
		sr.SetName(sym.Name) //nolint:errcheck
		sr.SetKind(uint8(sym.Kind))
		sr.SetContainerName(sym.ContainerName)      //nolint:errcheck
		sr.SetPath(lsp.URIToPath(sym.Location.URI)) //nolint:errcheck
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
		sr.SetName(sym.Name) //nolint:errcheck
		sr.SetKind(uint8(sym.Kind))
		sr.SetContainerName(sym.ContainerName)      //nolint:errcheck
		sr.SetPath(lsp.URIToPath(sym.Location.URI)) //nolint:errcheck
		sr.SetLine(uint32(sym.Location.Range.Start.Line))
		sr.SetCol(uint32(sym.Location.Range.Start.Character))
	}
	return nil
}

func (s *editorService) Format(_ context.Context, call proto.EditorService_format) error {
	// Format blocks on a synchronous LSP round trip (up to 10s — see
	// lsp.Client.Format) when no dedicated formatter is configured. Without
	// this, capnp serializes all calls on a connection behind it, so a slow
	// LSP formatter freezes typing (ApplyOp) for the whole client window.
	call.Go()

	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	content := entry.buf.Content()
	baseBuf := entry.buf
	baseVersion := baseBuf.Version()
	s.mu.Unlock()

	formatted, changed, fmtErr := s.fmtMgr.Format(path, content)

	noFormatter := errors.Is(fmtErr, format.ErrNoFormatter)
	if fmtErr != nil && !noFormatter {
		return fmtErr
	}

	if changed {
		s.mu.Lock()
		entry, ok = s.buffers[bufID]
		// Both checks matter: version alone isn't enough because
		// document.New() always starts a fresh buffer at version 0, so a
		// buffer swapped in by something else (e.g. a second concurrent
		// Format call finishing first) could coincidentally match
		// baseVersion despite being a different buffer entirely.
		if ok && entry.buf == baseBuf && entry.buf.Version() == baseVersion {
			newBuf := document.New(path, formatted)
			newBuf.MarkDirty()
			entry.buf = newBuf
			entry.generation++
			s.buffers[bufID] = entry
			s.mu.Unlock()
			go s.lspMgr.DidChange(path, formatted)
		} else {
			// The buffer changed while formatting ran outside the lock
			// (e.g. a keystroke's ApplyOp landed during format-on-save) —
			// "formatted" no longer reflects the buffer's current content.
			// Discard it rather than clobbering the newer edit; the next
			// Format call will format the buffer's actual current state.
			s.mu.Unlock()
			changed = false
		}
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

// lspEditsForURI extracts the TextEdits for uri from a WorkspaceEdit,
// checking documentChanges (used by gopls) before the legacy changes map.
func lspEditsForURI(edit *lsp.WorkspaceEdit, uri string) []lsp.TextEdit {
	if edit == nil {
		return nil
	}
	for _, dc := range edit.DocumentChanges {
		if dc.TextDocument.URI == uri {
			return dc.Edits
		}
	}
	return edit.Changes[uri]
}

func (s *editorService) LspCodeActions(_ context.Context, call proto.EditorService_lspCodeActions) error {
	args := call.Args()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())
	endLine := int(args.EndLine())
	endCol := int(args.EndCol())

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

	actions, err := s.lspMgr.CodeActions(path, line, col, endLine, endCol)
	if err != nil || len(actions) == 0 {
		return err
	}

	// Collect edits from either the legacy changes map or documentChanges array.
	uri := "file://" + path
	type actionEdits struct {
		action lsp.CodeAction
		edits  []lsp.TextEdit
	}
	var applicable []actionEdits
	for _, a := range actions {
		edits := lspEditsForURI(a.Edit, uri)
		if len(edits) > 0 {
			applicable = append(applicable, actionEdits{a, edits})
		}
	}
	if len(applicable) == 0 {
		return nil
	}

	list, err := res.NewActions(int32(len(applicable)))
	if err != nil {
		return err
	}
	for i, ae := range applicable {
		item := list.At(i)
		if err := item.SetTitle(ae.action.Title); err != nil {
			return err
		}
		if err := item.SetKind(ae.action.Kind); err != nil {
			return err
		}
		edits := ae.edits
		el, err := item.NewEdits(int32(len(edits)))
		if err != nil {
			return err
		}
		for j, e := range edits {
			ev := el.At(j)
			ev.SetFromLine(uint32(e.Range.Start.Line))
			ev.SetFromCol(uint32(e.Range.Start.Character))
			ev.SetToLine(uint32(e.Range.End.Line))
			ev.SetToCol(uint32(e.Range.End.Character))
			if err := ev.SetNewText(e.NewText); err != nil {
				return err
			}
		}
	}
	return nil
}

// LspOrganizeImports requests "source.organizeImports" from bufId's
// language server over the whole file and returns the resulting edits (if
// any) for the client to apply itself — mirrors LspCodeActions' edit
// extraction/conversion rather than applying server-side, so the change
// goes through the client's normal undo-aware batch path like every other
// LSP-edit-producing command.
func (s *editorService) LspOrganizeImports(_ context.Context, call proto.EditorService_lspOrganizeImports) error {
	bufID := call.Args().BufId()

	s.mu.Lock()
	entry, ok := s.buffers[bufID]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("unknown buffer %d", bufID)
	}
	path := entry.buf.Path()
	lineCount := entry.buf.LineCount()
	s.mu.Unlock()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}

	actions, err := s.lspMgr.OrganizeImports(path, lineCount)
	if err != nil || len(actions) == 0 {
		return err
	}

	uri := "file://" + path
	var edits []lsp.TextEdit
	for _, a := range actions {
		if e := lspEditsForURI(a.Edit, uri); len(e) > 0 {
			edits = e
			break
		}
	}
	if len(edits) == 0 {
		return nil
	}

	el, err := res.NewEdits(int32(len(edits)))
	if err != nil {
		return err
	}
	for j, e := range edits {
		ev := el.At(j)
		ev.SetFromLine(uint32(e.Range.Start.Line))
		ev.SetFromCol(uint32(e.Range.Start.Character))
		ev.SetToLine(uint32(e.Range.End.Line))
		ev.SetToCol(uint32(e.Range.End.Character))
		if err := ev.SetNewText(e.NewText); err != nil {
			return err
		}
	}
	return nil
}

// lspEditsByURI groups edit's per-file TextEdits by URI, preferring
// documentChanges (used by gopls) over the legacy changes map — mirrors
// lspEditsForURI but collects every file touched, not just one.
func lspEditsByURI(edit *lsp.WorkspaceEdit) map[string][]lsp.TextEdit {
	if edit == nil {
		return nil
	}
	if len(edit.DocumentChanges) > 0 {
		out := make(map[string][]lsp.TextEdit, len(edit.DocumentChanges))
		for _, dc := range edit.DocumentChanges {
			out[dc.TextDocument.URI] = dc.Edits
		}
		return out
	}
	return edit.Changes
}

// workspaceEditItemsFromLSP converts path's TextEdits (from an LSP rename's
// WorkspaceEdit) into workspaceEditItems by reading path's current content
// (from its open buffer if any, else disk) to capture the text each edit
// replaces — LSP TextEdits carry only a range and the new text, not the old.
// Multi-line edits are skipped: a rename only ever replaces a single
// identifier on one line, so this keeps the conversion simple and safe.
func (s *editorService) workspaceEditItemsFromLSP(path string, edits []lsp.TextEdit) ([]workspaceEditItem, error) {
	canonPath := canonicalPath(path)
	s.mu.Lock()
	var content string
	var found bool
	for _, e := range s.buffers {
		if e.canonPath == canonPath {
			content, found = e.buf.Content(), true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		content = string(data)
	}
	lines := strings.Split(content, "\n")

	items := make([]workspaceEditItem, 0, len(edits))
	for i, e := range edits {
		if e.Range.Start.Line != e.Range.End.Line {
			continue
		}
		ln := e.Range.Start.Line
		if ln < 0 || ln >= len(lines) {
			continue
		}
		lineRunes := []rune(lines[ln])
		start, end := e.Range.Start.Character, e.Range.End.Character
		if start < 0 || end > len(lineRunes) || start > end {
			continue
		}
		items = append(items, workspaceEditItem{
			origIdx: i,
			line:    ln,
			col:     start,
			oldText: string(lineRunes[start:end]),
			newText: e.NewText,
		})
	}
	return items, nil
}

// LspRename renames the symbol at (line, col) via the language server and
// applies the resulting edits across every affected file, reusing the same
// per-path apply logic as ApplyWorkspaceEdits (open buffers edited in place,
// other files patched on disk).
func (s *editorService) LspRename(_ context.Context, call proto.EditorService_lspRename) error {
	args := call.Args()
	clientID := args.ClientId()
	bufID := args.BufId()
	line := int(args.Line())
	col := int(args.Col())
	newName, err := args.NewName()
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
	content := entry.buf.Content()
	s.mu.Unlock()

	if line >= 0 && line < entry.buf.LineCount() {
		serverLog("LspRename: DEBUG path=%q line=%d col=%d target-line-content=%q buf-version=%d", path, line, col, entry.buf.Line(line), entry.buf.Version())
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}

	// RenameAfterChange (not a separate DidChange + Rename) ensures gopls
	// (or whichever server) has this buffer's *current* content before
	// computing the rename, and that nothing else can slip a stale
	// DidChange in between the two — see its doc comment. A rename issued
	// immediately after an edit (e.g. right after Extract Function) has to
	// guard against exactly that, or it silently computes against a stale
	// pre-edit view of the file, missing occurrences like the
	// just-introduced definition.
	edit, err := s.lspMgr.RenameAfterChange(path, content, line, col, newName)
	if err != nil {
		serverLog("LspRename: path=%q line=%d col=%d newName=%q failed: %v", path, line, col, newName, err)
		return err
	}
	if edit == nil {
		serverLog("LspRename: path=%q line=%d col=%d newName=%q: server returned no edit", path, line, col, newName)
		return nil
	}

	byURI := lspEditsByURI(edit)
	if len(byURI) == 0 {
		return nil
	}

	byPath := make(map[string][]workspaceEditItem, len(byURI))
	var order []string
	for uri, edits := range byURI {
		p := lsp.URIToPath(uri)
		items, err := s.workspaceEditItemsFromLSP(p, edits)
		if err != nil || len(items) == 0 {
			continue
		}
		byPath[p] = items
		order = append(order, p)
	}

	var appliedCount, fileCount uint32
	for _, path := range order {
		items := byPath[path]
		sort.Slice(items, func(a, b int) bool {
			if items[a].line != items[b].line {
				return items[a].line < items[b].line
			}
			return items[a].col < items[b].col
		})

		applied, _, err := s.applyItemsToPath(clientID, path, items)
		if err != nil || applied == 0 {
			continue
		}
		appliedCount += uint32(applied)
		fileCount++
	}

	res.SetAppliedCount(appliedCount)
	res.SetFileCount(fileCount)
	return nil
}
