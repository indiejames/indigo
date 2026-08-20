package client

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/document"
)

func (m Model) fetchDiagnostics() tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := m.rpc.GetDiagnostics(ctx, bufID)
		if err != nil {
			return nil
		}
		return diagnosticsMsg{bufID: bufID, diags: result.Diags, lspReady: result.LspReady}
	}
}

// fetchWorkspaceDiagnosticsSummary polls the cheap counts-only workspace
// diagnostic summary (see RPC.GetWorkspaceDiagnosticsSummary) for the
// status bar's project-wide indicator. Unlike fetchDiagnostics this isn't
// scoped to any one buffer, so there's no bufID to stamp or check.
func (m Model) fetchWorkspaceDiagnosticsSummary() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		summary, err := m.rpc.GetWorkspaceDiagnosticsSummary(ctx)
		if err != nil {
			return nil
		}
		return workspaceDiagSummaryMsg{summary: summary}
	}
}

// fetchDecorations polls all plugin DecorationProviders for the current buffer/viewport.
// The current viewport is sent inline before fetching so the server always uses
// an up-to-date range — sending both from the same goroutine guarantees ordering.
func (m Model) fetchDecorations() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	bufID := m.bufID
	topLine := uint32(m.topLine)
	height := uint32(m.height)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.UpdateViewport(ctx, topLine, height)
		items, err := m.rpc.GetDecorations(ctx, bufID)
		if err != nil {
			return nil
		}
		return decorationsMsg{bufID: bufID, items: items}
	}
}

// updateViewportCmd fires an UpdateViewport RPC so the server knows where the
// client is scrolled. Fire-and-forget: no message is returned on completion.
func (m Model) updateViewportCmd() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	topLine := uint32(m.topLine)
	height := uint32(m.height)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.UpdateViewport(ctx, topLine, height)
		return nil
	}
}

// fetchInlayHints polls the language server for inlay hints (inferred types,
// parameter names) in the current viewport. Skipped when the feature is
// disabled in config or there's no RPC connection. topLine/height give an
// approximate line range — soft-wrap means a screen row isn't exactly one
// buffer line, but a small over-fetch at the viewport edges is harmless.
func (m Model) fetchInlayHints() tea.Cmd {
	if m.rpc == nil || m.cfg == nil || !m.cfg.InlayHints {
		return nil
	}
	bufID := m.bufID
	startLine := m.topLine
	endLine := m.topLine + m.visibleLines()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		items, err := m.rpc.InlayHints(ctx, bufID, startLine, 0, endLine, 0)
		if err != nil {
			return nil
		}
		return inlayHintsMsg{bufID: bufID, items: items}
	}
}

// fetchSemanticTokens polls the language server for semantic tokens
// (LSP-derived syntax coloring) in the current viewport. Skipped when the
// feature is disabled in config or there's no RPC connection.
func (m Model) fetchSemanticTokens() tea.Cmd {
	if m.rpc == nil || m.cfg == nil || !m.cfg.SemanticTokens {
		return nil
	}
	bufID := m.bufID
	startLine := m.topLine
	endLine := m.topLine + m.visibleLines()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		items, err := m.rpc.SemanticTokensRange(ctx, bufID, startLine, 0, endLine, 0)
		if err != nil {
			return nil
		}
		return semanticTokensMsg{bufID: bufID, items: items}
	}
}

func (m Model) fetchHover() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := m.rpc.Hover(ctx, bufID, line, col)
		if err != nil {
			return errorMsg{err}
		}
		return hoverMsg{result}
	}
}

func (m Model) fetchSignatureHelp() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		sh, err := m.rpc.SignatureHelp(ctx, bufID, line, col)
		if err != nil || len(sh.Signatures) == 0 {
			return sigHelpMsg{nil}
		}
		return sigHelpMsg{&sh}
	}
}

func (m Model) fetchCompletions() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		items, err := m.rpc.Complete(ctx, bufID, line, col)
		if err != nil {
			return nil
		}
		return completionsMsg{items}
	}
}

// resolveCompletionCmd resolves the accepted completion item off the UI thread
// to fetch its additionalTextEdits (the auto-import line), then applies it via
// completionResolvedMsg. at and prefix are captured at accept time so the apply
// lands correctly regardless of later cursor movement. On error the item is
// applied unresolved, so the primary insert still happens without the import.
// RPC.ResolveCompletion itself retains the pre-resolve TextEdit when the
// resolved item doesn't supply its own (see its doc comment).
func (m Model) resolveCompletionCmd(item ClientCompletion, at document.Pos, prefix string) tea.Cmd {
	bufID := m.bufID
	rpc := m.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		resolved, err := rpc.ResolveCompletion(ctx, bufID, item)
		if err != nil {
			resolved = item
		}
		return completionResolvedMsg{item: resolved, at: at, prefix: prefix, bufID: bufID}
	}
}

func (m Model) fetchDefinition() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		loc, found, err := m.rpc.Definition(ctx, bufID, line, col)
		if err != nil {
			return nil
		}
		return definitionMsg{loc: loc, found: found}
	}
}

func (m Model) fetchReferences() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		refs, err := m.rpc.References(ctx, bufID, line, col)
		if err != nil {
			return nil
		}
		return referencesMsg{refs: refs}
	}
}

func (m Model) fetchDocSymbols() tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		syms, err := m.rpc.DocumentSymbols(ctx, bufID)
		if err != nil {
			return nil
		}
		return docSymbolsMsg{syms: syms}
	}
}

func (m Model) fetchFormat(thenSave bool) tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		content, changed, noFormatter, err := m.rpc.Format(ctx, bufID)
		if err != nil {
			return formatResultMsg{bufID: bufID, thenSave: thenSave, err: err}
		}
		return formatResultMsg{bufID: bufID, content: content, changed: changed, thenSave: thenSave, noFormatter: noFormatter}
	}
}

// fixableDecorationAtCursor returns the first fixable underline decoration covering the cursor, or nil.
func (m Model) fixableDecorationAtCursor() *ClientDecoration {
	for i := range m.decorations {
		d := &m.decorations[i]
		if !d.Fixable || d.Kind != ClientDecorationUnderline {
			continue
		}
		if int(d.Line) == m.cursor.Line && m.cursor.Col >= int(d.Col) && m.cursor.Col < int(d.EndCol) {
			return d
		}
	}
	return nil
}

// fetchPluginBindings fetches fresh plugin key bindings from the server and
// opens the help popup once the response arrives. Called when ? is pressed so
// that plugins registered after startup are always visible.
func (m Model) fetchPluginBindings() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		bindings, err := m.rpc.GetPluginBindings(ctx)
		if err != nil {
			return pluginBindingsMsg{bindings: m.pluginBindings} // fall back to cached
		}
		return pluginBindingsMsg{bindings: bindings}
	}
}

// doMoveFunctionToFile finds the function surrounding the cursor (via the
// language's tree-sitter grammar, the same text object "af"/"if" use) and
// moves it server-side to destPath, appending to the end of the file (which
// is created if it doesn't exist yet). Imports and other cross-file
// references are not fixed up.
func (m Model) doMoveFunctionToFile(destPath string) tea.Cmd {
	if m.hlr == nil {
		return func() tea.Msg {
			return moveFunctionDoneMsg{err: fmt.Errorf("no syntax support for this file type")}
		}
	}
	to, ok := m.hlr.TextObjectAround([]byte(m.buf.Content()), m.cursor.Line, m.cursor.Col, "function")
	if !ok {
		return func() tea.Msg {
			return moveFunctionDoneMsg{err: fmt.Errorf("no function found around the cursor")}
		}
	}
	rpc := m.rpc
	bufID := m.bufID
	workDir := m.workDir
	fromLine, fromCol := to.StartLine, to.StartCol
	toLine, toCol := to.EndLine, to.EndCol+1 // TextObject.EndCol is inclusive; ops are exclusive
	return func() tea.Msg {
		abs, err := resolveDestPath(workDir, destPath)
		if err != nil {
			return moveFunctionDoneMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := rpc.MoveTextToFile(ctx, bufID, fromLine, fromCol, toLine, toCol, abs); err != nil {
			return moveFunctionDoneMsg{err: err}
		}
		return moveFunctionDoneMsg{destPath: abs}
	}
}

// resolveDestPath makes path absolute, joining it against workDir first when
// it's relative (mirroring how :w/:wq resolve a save-as path).
func resolveDestPath(workDir, path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(filepath.Join(workDir, path))
}

// doRenameSymbol renames the symbol at the cursor to newName via the
// language server, applying the resulting edits across every affected file
// server-side. Any already-open buffer touched by the rename (including
// this one) picks up the change through the normal getUpdates poll, exactly
// like a workspace-wide search & replace.
func (m Model) doRenameSymbol(newName string) tea.Cmd {
	rpc := m.rpc
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		applied, files, err := rpc.LspRename(ctx, bufID, line, col, newName)
		return renameSymbolDoneMsg{applied: applied, files: files, err: err}
	}
}

// doOrganizeImports requests "source.organizeImports" from the language
// server for the current buffer and, if it returns any edits, applies them
// through the normal undo-aware batch path (see applyLspEdits) — unlike the
// F-popup Code Actions flow, this applies directly rather than showing a
// picker, since a server normally returns at most one organize-imports
// result.
func (m Model) doOrganizeImports() tea.Cmd {
	rpc := m.rpc
	bufID := m.bufID
	bufVersion := m.buf.Version()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		edits, err := rpc.LspOrganizeImports(ctx, bufID)
		return organizeImportsMsg{bufID: bufID, bufVersion: bufVersion, edits: edits, err: err}
	}
}

// codeActionRange returns the request range for LSP code actions: the active
// selection's ordered bounds if one exists (letting the server offer
// range-only actions like Extract Function/Extract Variable), or a
// zero-width point at the cursor otherwise. Selection.Anchor/Head are
// inclusive on both ends; LSP ranges are exclusive on the end, hence the +1.
func codeActionRange(cursor document.Pos, sel *Selection) (startLine, startCol, endLine, endCol int) {
	if sel == nil {
		return cursor.Line, cursor.Col, cursor.Line, cursor.Col
	}
	start, end := sel.ordered()
	return start.Line, start.Col, end.Line, end.Col + 1
}

// fetchFixes collects fix items from the fixable decoration at the cursor (if any)
// and context-sensitive actions from all registered action providers, then merges
// them into a single F-popup list.
func (m Model) fetchFixes() tea.Cmd {
	decor := m.fixableDecorationAtCursor()
	bufID := m.bufID
	at := m.cursor
	version := m.buf.Version()
	line := uint32(m.cursor.Line)
	col := uint32(m.cursor.Col)

	startLine, startCol, endLine, endCol := codeActionRange(m.cursor, m.sel)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var items []ClientFixItem

		// Decoration-based fixes from the plugin that owns the underline/marker.
		if decor != nil {
			fixes, err := m.rpc.GetPluginFixes(ctx, decor.PluginName, decor.FixData)
			if err == nil {
				for i, fix := range fixes {
					items = append(items, ClientFixItem{
						Label:     fix.Label,
						Replace:   fix.Replace,
						FromLine:  int(decor.Line),
						FromCol:   int(decor.Col),
						ToLine:    int(decor.Line),
						ToCol:     int(decor.EndCol),
						Plugin:    decor.PluginName,
						FixData:   decor.FixData,
						OrigIndex: i,
					})
				}
			}
		}

		// Context-sensitive actions from all registered action providers.
		actions, err := m.rpc.GetPluginActions(ctx, bufID, line, col)
		if err == nil {
			items = append(items, actions...)
		}

		// LSP code actions (quick-fixes and range-based refactors from the
		// language server).
		lspActions, err := m.rpc.LspCodeActions(ctx, bufID, startLine, startCol, endLine, endCol)
		if err == nil {
			for _, a := range lspActions {
				items = append(items, ClientFixItem{
					Label:    a.Title,
					LspEdits: a.Edits,
					LspKind:  a.Kind,
				})
			}
		}

		return fixItemsMsg{items: items, decor: decor, bufID: bufID, at: at, version: version}
	}
}

// applyLspEdits applies LSP code-action edits through the normal undo-aware
// batch path (applyBatch), so the change marks the buffer dirty and is
// undoable like any other edit — unlike the old path, which sent raw ops to
// the server and rebuilt the buffer wholesale, wiping the undo stack. Edits
// are converted to delete+insert op pairs in reverse document order so
// earlier edits don't shift later positions.
func applyLspEdits(m Model, edits []ClientLspEdit) (Model, tea.Cmd) {
	clientID := m.clientID()
	ops := make([]document.Op, 0, len(edits)*2)
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		if e.FromLine != e.ToLine || e.FromCol != e.ToCol {
			ops = append(ops, document.Op{
				Type:     document.OpDelete,
				FromLine: e.FromLine,
				FromCol:  e.FromCol,
				ToLine:   e.ToLine,
				ToCol:    e.ToCol,
				ClientID: clientID,
			})
		}
		if e.NewText != "" {
			ops = append(ops, document.Op{
				Type:       document.OpInsert,
				InsertLine: e.FromLine,
				InsertCol:  e.FromCol,
				InsertText: e.NewText,
				ClientID:   clientID,
			})
		}
	}
	return applyBatch(m, ops)
}

// doApplyExtractAndRename applies a pending range-extract action's edits
// (Extract Function/Extract Variable), then immediately renames the
// server's default name for the newly introduced symbol (e.g.
// "newFunction"/"newVar") to newName via a real LSP rename — so the user
// only ever sees the final name, and the rename is cross-file-capable like
// any other Rename Symbol, not a local text-only substitution.
func (m Model) doApplyExtractAndRename(edits []ClientLspEdit, kind, newName string) (Model, tea.Cmd) {
	oldContent := m.buf.Content()
	m2, applyCmd := applyLspEdits(m, edits)

	name := ""
	if len(edits) == 1 {
		name = detectExtractedName(oldContent, edits[0].NewText)
	}
	if name == "" {
		name = defaultExtractedName(kind)
	}
	if name == "" {
		m2.status = "Extracted (couldn't determine the new name to rename automatically)"
		return m2, applyCmd
	}

	positions := findWholeWordOccurrences(m2, name)
	if len(positions) == 0 {
		m2.status = "Extracted (couldn't locate the new symbol to rename automatically)"
		return m2, applyCmd
	}
	// Definition-vs-call-site order isn't guaranteed (gopls typically places
	// the extracted definition after the call site in the file), but any
	// occurrence resolves the same symbol for LSP rename purposes.
	m2.cursor = positions[0]

	// tea.Sequence, not tea.Batch: the rename request must not reach the
	// server until the extraction's ops have actually been applied and
	// acknowledged there — otherwise it can race the (fire-and-forget)
	// gopls notification and rename against a stale pre-extraction view of
	// the file, silently missing occurrences.
	return m2, tea.Sequence(applyCmd, m2.doRenameSymbol(newName))
}

// applyFixCmd applies the selected fix: either a direct text replacement or a
// plugin callback. LSP-edit items are handled synchronously by applyLspEdits
// in handleFixPopup instead, since they must update the model's undo stack.
func (m Model) applyFixCmd(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.fixItems) {
		return nil
	}
	item := m.fixItems[idx]
	if item.Replace != "" {
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if item.FromLine != item.ToLine || item.FromCol != item.ToCol {
				delOp := document.Op{
					Type:     document.OpDelete,
					FromLine: item.FromLine,
					FromCol:  item.FromCol,
					ToLine:   item.ToLine,
					ToCol:    item.ToCol,
					ClientID: m.rpc.ClientID(),
				}
				if _, err := m.rpc.ApplyOp(ctx, m.bufID, delOp, m.generation); err != nil {
					return errorMsg{err}
				}
			}
			insOp := document.Op{
				Type:       document.OpInsert,
				InsertLine: item.FromLine,
				InsertCol:  item.FromCol,
				InsertText: item.Replace,
				ClientID:   m.rpc.ClientID(),
			}
			if _, err := m.rpc.ApplyOp(ctx, m.bufID, insOp, m.generation); err != nil {
				return errorMsg{err}
			}
			return nil
		}
	}
	if item.IsAction {
		bufID := m.bufID
		line := uint32(m.cursor.Line)
		col := uint32(m.cursor.Col)
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.rpc.ApplyPluginAction(ctx, item.Plugin, bufID, line, col, uint32(item.OrigIndex)); err != nil {
				return errorMsg{err}
			}
			return nil
		}
	}
	// Decoration-based fix with plugin callback.
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.ApplyPluginFix(ctx, item.Plugin, item.FixData, uint32(item.OrigIndex)); err != nil {
			return errorMsg{err}
		}
		return nil
	}
}

// currentWordPrefix returns the identifier fragment immediately before the cursor.
func (m Model) currentWordPrefix() string {
	line := m.buf.Line(m.cursor.Line)
	runes := []rune(line)
	col := min(m.cursor.Col, len(runes))
	start := col
	for start > 0 && isWordChar(runes[start-1]) {
		start--
	}
	return string(runes[start:col])
}

// diagsAtPos returns diagnostics whose underline range covers (line, col), most severe first.
// A diagnostic covers col when d.Col <= col < d.EndCol (or endCol is clamped to col+1 for zero-width) —
// mirrors render.go's per-line column logic for painting a (possibly
// multi-line) diagnostic's underline exactly, so hovering/auto-popup
// detection matches what's actually drawn on screen: a multi-line
// diagnostic used to only ever match on its starting line (d.Line != line
// ignored d.EndLine entirely), so the popup silently never showed for the
// cursor on any of its other lines even though the underline was visibly
// there.
func (m Model) diagsAtPos(line, col int) []ClientDiag {
	var out []ClientDiag
	for _, d := range m.diagnostics {
		var match bool
		switch {
		case d.Line == line && d.EndLine == line:
			end := d.EndCol
			if end <= d.Col {
				end = d.Col + 1
			}
			match = col >= d.Col && col < end
		case d.Line == line && d.EndLine > line:
			match = col >= d.Col // rest of the (first) line
		case d.Line < line && d.EndLine == line:
			match = col < d.EndCol // up to the endpoint on the (last) line
		case d.Line < line && d.EndLine > line:
			match = true // an interior line is covered in full
		}
		if match {
			out = append(out, d)
		}
	}
	sortDiags(out)
	return out
}

func sortDiags(diags []ClientDiag) {
	for i := 1; i < len(diags); i++ {
		for j := i; j > 0 && diags[j].Severity < diags[j-1].Severity; j-- {
			diags[j], diags[j-1] = diags[j-1], diags[j]
		}
	}
}

// expandDiags widens zero-width (point) diagnostic ranges to cover the full
// identifier/token at that position, so the underline and popup trigger span
// the whole token rather than just a single character.
func (m Model) expandDiags(diags []ClientDiag) []ClientDiag {
	out := make([]ClientDiag, len(diags))
	copy(out, diags)
	for i, d := range out {
		if d.EndLine == d.Line && d.EndCol <= d.Col && d.Line < m.buf.LineCount() {
			runes := []rune(m.buf.Line(d.Line))
			end := d.Col
			if end < len(runes) && isIdentRune(runes[end]) {
				for end < len(runes) && isIdentRune(runes[end]) {
					end++
				}
			} else {
				end = d.Col + 1
			}
			out[i].EndCol = end
		}
	}
	return out
}

func isIdentRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}
