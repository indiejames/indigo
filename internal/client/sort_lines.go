package client

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
)

// executeSortLinesAscending sorts the selected lines (or, with no selection,
// leaves the single cursor line untouched) into ascending lexicographic
// order.
func executeSortLinesAscending(m Model) (tea.Model, tea.Cmd) {
	return sortLines(m, false)
}

// executeSortLinesDescending sorts the selected lines into descending
// lexicographic order.
func executeSortLinesDescending(m Model) (tea.Model, tea.Cmd) {
	return sortLines(m, true)
}

// sortLines replaces the selected line range (via selectionLineRange; a
// no-selection single line is a no-op, since there's nothing to reorder)
// with the same lines sorted lexicographically, ascending or descending per
// desc. The sort is stable so equal lines keep their relative order. This
// mirrors moveLines' whole-line delete/insert shape but skips its
// reindent machinery: lines keep their own indentation, only their order
// changes.
//
// The line range itself doesn't move or resize, only its content, so the
// selection is left covering the same [startLine, endLine] span afterward
// (normalized to a whole-line selection, mirroring selectLine's shape)
// rather than cleared — the user can chain another sort direction, or
// immediately act on the now-sorted block without reselecting it. The
// original Anchor/Head direction is preserved (an upward selection stays
// anchored at the bottom), matching how moveLines/executeIndent shift an
// existing selection rather than re-deriving a fixed direction.
func sortLines(m Model, desc bool) (Model, tea.Cmd) {
	startLine, endLine := m.selectionLineRange()
	if startLine == endLine {
		return m, nil
	}
	// selectionLineRange only returns startLine != endLine when m.sel is a
	// real multi-line selection (a nil sel or single-line sel both collapse
	// startLine == endLine above), so m.sel is guaranteed non-nil here.
	anchorAtStart := m.sel.Anchor.Line < m.sel.Head.Line ||
		(m.sel.Anchor.Line == m.sel.Head.Line && m.sel.Anchor.Col <= m.sel.Head.Col)

	lines := make([]string, 0, endLine-startLine+1)
	for ln := startLine; ln <= endLine; ln++ {
		lines = append(lines, m.buf.Line(ln))
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if desc {
			return lines[i] > lines[j]
		}
		return lines[i] < lines[j]
	})

	lc := m.buf.LineCount()
	delOp := document.Op{ClientID: m.clientID(), Type: document.OpDelete, FromLine: startLine, FromCol: 0}
	newText := strings.Join(lines, "\n")
	if endLine+1 < lc {
		delOp.ToLine, delOp.ToCol = endLine+1, 0
		newText += "\n"
	} else {
		delOp.ToLine, delOp.ToCol = endLine, m.buf.LineLen(endLine)
	}
	insOp := document.Op{
		ClientID:   m.clientID(),
		Type:       document.OpInsert,
		InsertLine: startLine,
		InsertCol:  0,
		InsertText: newText,
	}

	m, cmd := applyBatch(m, []document.Op{delOp, insOp})
	startPos := document.Pos{Line: startLine, Col: 0}
	endPos := document.Pos{Line: endLine, Col: max(0, m.buf.LineLen(endLine)-1)}
	if anchorAtStart {
		m.sel = &Selection{Anchor: startPos, Head: endPos, IsLine: true}
	} else {
		m.sel = &Selection{Anchor: endPos, Head: startPos, IsLine: true}
	}
	m.cursor = m.sel.Head
	return m, cmd
}
