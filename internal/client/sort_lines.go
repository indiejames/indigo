package client

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

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
// immediately act on the now-sorted block without reselecting it.
func sortLines(m Model, desc bool) (Model, tea.Cmd) {
	startLine, endLine := m.selectionLineRange()
	if startLine == endLine {
		return m, nil
	}

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
	m.sel = &Selection{
		Anchor: document.Pos{Line: startLine, Col: 0},
		Head:   document.Pos{Line: endLine, Col: max(0, m.buf.LineLen(endLine)-1)},
		IsLine: true,
	}
	m.cursor = m.sel.Head
	return m, cmd
}
