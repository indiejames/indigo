package client

import (
	"context"
	"strings"
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
			return errorMsg{err}
		}
		return formatResultMsg{content: content, changed: changed, thenSave: thenSave, noFormatter: noFormatter}
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

// fetchFixes collects fix items from the fixable decoration at the cursor (if any)
// and context-sensitive actions from all registered action providers, then merges
// them into a single F-popup list.
func (m Model) fetchFixes() tea.Cmd {
	decor := m.fixableDecorationAtCursor()
	bufID := m.bufID
	line := uint32(m.cursor.Line)
	col := uint32(m.cursor.Col)
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

		// LSP code actions (quick-fixes from the language server).
		lspActions, err := m.rpc.LspCodeActions(ctx, bufID, int(line), int(col))
		if err == nil {
			for _, a := range lspActions {
				items = append(items, ClientFixItem{
					Label:    a.Title,
					LspEdits: a.Edits,
				})
			}
		}

		return fixItemsMsg{items: items, decor: decor}
	}
}

// applyLspEditsToContent applies LSP edits (in reverse order to preserve positions) to a
// content string and returns the result.
func applyLspEditsToContent(content string, edits []ClientLspEdit) string {
	lines := strings.Split(content, "\n")
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		if e.FromLine >= len(lines) {
			continue
		}
		toLine := e.ToLine
		if toLine >= len(lines) {
			toLine = len(lines) - 1
		}
		startRunes := []rune(lines[e.FromLine])
		endRunes := []rune(lines[toLine])
		sc := min(e.FromCol, len(startRunes))
		ec := min(e.ToCol, len(endRunes))
		prefix := string(startRunes[:sc])
		suffix := string(endRunes[ec:])
		replacement := strings.Split(prefix+e.NewText+suffix, "\n")
		updated := make([]string, 0, len(lines)-(toLine-e.FromLine+1)+len(replacement))
		updated = append(updated, lines[:e.FromLine]...)
		updated = append(updated, replacement...)
		updated = append(updated, lines[toLine+1:]...)
		lines = updated
	}
	return strings.Join(lines, "\n")
}

// applyFixCmd applies the selected fix: either a direct text replacement or a plugin callback.
func (m Model) applyFixCmd(idx int) tea.Cmd {
	if idx < 0 || idx >= len(m.fixItems) {
		return nil
	}
	item := m.fixItems[idx]
	if len(item.LspEdits) > 0 {
		edits := item.LspEdits
		bufID := m.bufID
		newContent := applyLspEditsToContent(m.buf.Content(), edits)
		clientID := m.rpc.ClientID()
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Apply in reverse order so earlier edits don't shift later line numbers.
			for i := len(edits) - 1; i >= 0; i-- {
				e := edits[i]
				if e.FromLine != e.ToLine || e.FromCol != e.ToCol {
					delOp := document.Op{
						Type:     document.OpDelete,
						FromLine: e.FromLine,
						FromCol:  e.FromCol,
						ToLine:   e.ToLine,
						ToCol:    e.ToCol,
						ClientID: clientID,
					}
					if _, err := m.rpc.ApplyOp(ctx, bufID, delOp); err != nil {
						return errorMsg{err}
					}
				}
				if e.NewText != "" {
					insOp := document.Op{
						Type:       document.OpInsert,
						InsertLine: e.FromLine,
						InsertCol:  e.FromCol,
						InsertText: e.NewText,
						ClientID:   clientID,
					}
					if _, err := m.rpc.ApplyOp(ctx, bufID, insOp); err != nil {
						return errorMsg{err}
					}
				}
			}
			return formatResultMsg{content: newContent, changed: true}
		}
	}
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
				if _, err := m.rpc.ApplyOp(ctx, m.bufID, delOp); err != nil {
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
			if _, err := m.rpc.ApplyOp(ctx, m.bufID, insOp); err != nil {
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
// A diagnostic covers col when d.Col <= col < d.EndCol (or endCol is clamped to col+1 for zero-width).
func (m Model) diagsAtPos(line, col int) []ClientDiag {
	var out []ClientDiag
	for _, d := range m.diagnostics {
		if d.Line != line {
			continue
		}
		end := d.EndCol
		if end <= d.Col {
			end = d.Col + 1
		}
		if col >= d.Col && col < end {
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
