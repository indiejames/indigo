package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
)

// sraFocus identifies which control in the search/replace dialog has focus.
type sraFocus int

const (
	sraFocusSearch sraFocus = iota
	sraFocusCase
	sraFocusRegex
	sraFocusToggle
	sraFocusReplace
	sraFocusAll
	sraFocusResults
)

// searchReplaceDialog is the 's'-triggered global search & replace popup.
type searchReplaceDialog struct {
	workDir string
	width   int
	height  int

	searchInput   textinput.Model
	replaceInput  textinput.Model
	caseSensitive bool
	useRegex      bool
	replaceOpen   bool
	focus         sraFocus

	searching bool
	searched  bool
	spinner   spinner.Model
	results   []GrepResult
	cursor    int
	viewport  viewport.Model

	errMsg string

	confirmingAll bool
	applyMsg      string
}

func newSearchReplaceDialog(workDir string, w, h int) *searchReplaceDialog {
	si := textinput.New()
	si.Placeholder = "Search"
	si.Prompt = ""
	si.Focus()

	ri := textinput.New()
	ri.Placeholder = "Replace"
	ri.Prompt = ""

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	d := &searchReplaceDialog{
		workDir:      workDir,
		width:        w,
		height:       h,
		searchInput:  si,
		replaceInput: ri,
		spinner:      sp,
		viewport:     viewport.New(dialogInnerW(w), 8),
		focus:        sraFocusSearch,
	}
	return d
}

func dialogInnerW(termW int) int {
	w := min(64, termW-12)
	return max(w, 24)
}

// ---- messages ----

type sraResultsMsg struct {
	results []GrepResult
	err     error
}

// bufferAppliedMsg carries the result of applying a search-and-replace edit
// to a buffer that was already open in this App.
type bufferAppliedMsg struct {
	idx                 int
	model               client.Model
	cmd                 tea.Cmd
	line, col, matchLen int
}

type sraSingleResultMsg struct {
	err error
	// Exactly one of these is set when err == nil.
	applied *bufferAppliedMsg
	opened  *bufferOpenedMsg
}

type sraApplyAllDoneMsg struct {
	applied int
	skipped int
	err     error
}

// ---- focus management ----

func (d *searchReplaceDialog) focusOrder() []sraFocus {
	order := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusToggle}
	if d.replaceOpen {
		order = append(order, sraFocusReplace, sraFocusAll)
	}
	if len(d.results) > 0 {
		order = append(order, sraFocusResults)
	}
	return order
}

func (d *searchReplaceDialog) setFocus(f sraFocus) {
	d.focus = f
	if f == sraFocusSearch {
		d.searchInput.Focus()
	} else {
		d.searchInput.Blur()
	}
	if f == sraFocusReplace {
		d.replaceInput.Focus()
	} else {
		d.replaceInput.Blur()
	}
}

func (d *searchReplaceDialog) advanceFocus(delta int) {
	order := d.focusOrder()
	idx := 0
	for i, f := range order {
		if f == d.focus {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(order)) % len(order)
	d.setFocus(order[idx])
}

// ---- search ----

func (a App) startSearchReplaceSearch(d *searchReplaceDialog) tea.Cmd {
	pattern := d.searchInput.Value()
	if pattern == "" {
		return nil
	}
	d.searching = true
	d.searched = false
	d.errMsg = ""
	d.results = nil
	d.cursor = 0
	workDir := d.workDir
	caseSensitive := d.caseSensitive
	useRegex := d.useRegex
	searchCmd := func() tea.Msg {
		results, err := searchWorkspaceExplicit(workDir, pattern, "", caseSensitive, useRegex)
		return sraResultsMsg{results: results, err: err}
	}
	return tea.Batch(searchCmd, d.spinner.Tick)
}

// oldTextOf returns the exact matched runes for r, used both for the on-screen
// diff and as the server-verified oldText of a WorkspaceEdit.
func oldTextOf(r GrepResult) string {
	lr := []rune(r.LineText)
	end := min(r.Col+r.MatchLen, len(lr))
	if r.Col < 0 || r.Col > end {
		return ""
	}
	return string(lr[r.Col:end])
}

func (d *searchReplaceDialog) refreshResultsView() {
	lines := make([]string, len(d.results))
	for i, r := range d.results {
		lines[i] = d.renderResultLine(r, i == d.cursor && d.focus == sraFocusResults)
	}
	d.viewport.SetContent(strings.Join(lines, "\n"))
	top, vh := d.viewport.YOffset, d.viewport.Height
	if vh <= 0 {
		return
	}
	if d.cursor < top {
		d.viewport.SetYOffset(d.cursor)
	} else if d.cursor >= top+vh {
		d.viewport.SetYOffset(d.cursor - vh + 1)
	}
}

func (d *searchReplaceDialog) moveCursor(delta int) {
	if len(d.results) == 0 {
		return
	}
	d.cursor = min(max(d.cursor+delta, 0), len(d.results)-1)
	d.refreshResultsView()
}

// ---- apply: single match ----

// openSearchReplaceMatch opens (or switches to) the file for the currently
// selected result and jumps to the match, without editing anything. Used
// when the replace field isn't toggled open, so results behave like a plain
// search that opens files on Enter.
func (a App) openSearchReplaceMatch(d *searchReplaceDialog) tea.Cmd {
	if d.cursor < 0 || d.cursor >= len(d.results) {
		return nil
	}
	r := d.results[d.cursor]
	absPath := filepath.Join(d.workDir, r.RelPath)
	matchLen := len([]rune(oldTextOf(r)))
	return a.doOpenFileAtMatch(absPath, r.Line, r.Col, matchLen)
}

func (a App) acceptSearchReplaceMatch(d *searchReplaceDialog) tea.Cmd {
	if d.focus != sraFocusResults || d.cursor < 0 || d.cursor >= len(d.results) {
		return nil
	}
	r := d.results[d.cursor]
	oldText := oldTextOf(r)
	newText := d.replaceInput.Value()
	absPath := filepath.Join(d.workDir, r.RelPath)
	line, col := r.Line, r.Col

	delOp := document.Op{Type: document.OpDelete, FromLine: line, FromCol: col, ToLine: line, ToCol: col + len([]rune(oldText))}
	insOp := document.Op{Type: document.OpInsert, InsertLine: line, InsertCol: col, InsertText: newText}

	// Already open in this App: apply through that Model's normal local-apply
	// + undo + server-send path so the tab and undo stack stay correct.
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			applied := &bufferAppliedMsg{idx: i, line: line, col: col, matchLen: len([]rune(newText))}
			applied.model, applied.cmd = m.ApplyExternalOps([]document.Op{delOp, insOp})
			return func() tea.Msg { return sraSingleResultMsg{applied: applied} }
		}
	}

	// Not open anywhere in this App: apply on the server first, then open
	// fresh so the new tab shows the post-edit content immediately.
	rpc := a.rpc
	cfg := a.cfg
	workDir := a.workDir
	matchLen := len([]rune(newText))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, _, _, _, _, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return sraSingleResultMsg{err: err}
		}
		if _, err := rpc.ApplyOps(ctx, bufID, []document.Op{delOp, insOp}); err != nil {
			return sraSingleResultMsg{err: err}
		}
		bufID, content, version, fromRecovery, _, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return sraSingleResultMsg{err: err}
		}
		m := client.New(rpc, bufID, content, version, absPath, workDir, cfg, fromRecovery)
		return sraSingleResultMsg{opened: &bufferOpenedMsg{model: m, line: line, col: col, matchLen: matchLen}}
	}
}

// ---- apply: all ----

func (a App) startApplyAll(d *searchReplaceDialog) tea.Cmd {
	edits := make([]client.WorkspaceEdit, len(d.results))
	for i, r := range d.results {
		edits[i] = client.WorkspaceEdit{
			Path:    filepath.Join(d.workDir, r.RelPath),
			Line:    r.Line,
			Col:     r.Col,
			OldText: oldTextOf(r),
			NewText: d.replaceInput.Value(),
		}
	}
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		applied, skippedIdx, err := rpc.ApplyWorkspaceEdits(ctx, edits)
		if err != nil {
			return sraApplyAllDoneMsg{err: err}
		}
		return sraApplyAllDoneMsg{applied: applied, skipped: len(skippedIdx)}
	}
}

// ---- rendering ----

var (
	sraBorderStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#4488CC"))
	sraBorderFocusStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#5FD7FF"))
	sraLabelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#AABBCC"))
	sraLabelFocusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FD7FF")).Bold(true)
	sraOldTextStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#CC6666")).Strikethrough(true)
	sraNewTextStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#66CC88")).Bold(true)
	sraSelStyle         = lipgloss.NewStyle().Background(lipgloss.Color("#2D5F8A")).Foreground(lipgloss.Color("#FFFFFF"))
	sraDimStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#778899"))
	sraErrStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B"))

	sraDialogBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#4488CC")).
		// Background(lipgloss.Color("#1E2A38")).
		// Background(lipgloss.Color("#000000")).
		Padding(0, 1)
)

func checkbox(label string, checked, focused bool) string {
	mark := " "
	if checked {
		mark = "x"
	}
	s := fmt.Sprintf("[%s] %s", mark, label)
	if focused {
		return sraLabelFocusStyle.Render(s)
	}
	return sraLabelStyle.Render(s)
}

func (d *searchReplaceDialog) renderResultLine(r GrepResult, selected bool) string {
	oldText := oldTextOf(r)
	newText := d.replaceInput.Value()
	loc := fmt.Sprintf("%s:%d:", r.RelPath, r.Line+1)
	prefix := strings.TrimLeft(r.LineText[:byteOffsetOfRune(r.LineText, r.Col)], " \t")
	showDiff := d.replaceOpen && newText != ""

	w := dialogInnerW(d.width) + 10

	if selected {
		// Plain text under the selection background — nesting the old/new
		// styling's own ANSI resets inside sraSelStyle would cut a visible
		// gap in the highlight.
		var plain string
		if showDiff {
			plain = fmt.Sprintf("%s %s%s → %s", loc, prefix, oldText, newText)
		} else {
			plain = fmt.Sprintf("%s %s%s", loc, prefix, oldText)
		}
		plain = ansiTruncate(plain, w)
		pad := max(0, w-lipgloss.Width(plain))
		return sraSelStyle.Render(plain + strings.Repeat(" ", pad))
	}

	var line string
	if showDiff {
		line = fmt.Sprintf("%s %s%s%s %s", loc, prefix, sraOldTextStyle.Render(oldText), " → ", sraNewTextStyle.Render(newText))
	} else {
		line = fmt.Sprintf("%s %s%s", loc, prefix, oldText)
	}
	if lipgloss.Width(line) > w {
		line = ansiTruncate(line, w)
	}
	return line
}

func byteOffsetOfRune(s string, runeIdx int) int {
	if runeIdx <= 0 {
		return 0
	}
	n := 0
	for i := range s {
		if n == runeIdx {
			return i
		}
		n++
	}
	return len(s)
}

func ansiTruncate(s string, w int) string {
	return ansi.Truncate(s, w, "…")
}

func (d *searchReplaceDialog) render() string {
	innerW := dialogInnerW(d.width)

	searchBorder := sraBorderStyle
	if d.focus == sraFocusSearch {
		searchBorder = sraBorderFocusStyle
	}
	searchBox := searchBorder.Width(innerW).Render(d.searchInput.View())
	searchRows := strings.Split(searchBox, "\n")
	searchRows[0] += "  " + checkbox("Aa", d.caseSensitive, d.focus == sraFocusCase)
	if len(searchRows) > 1 {
		searchRows[1] += "  " + checkbox(".*", d.useRegex, d.focus == sraFocusRegex)
	}

	toggleMark := "▸"
	if d.replaceOpen {
		toggleMark = "▾"
	}
	toggleStyle := sraLabelStyle
	if d.focus == sraFocusToggle {
		toggleStyle = sraLabelFocusStyle
	}
	toggleLine := toggleStyle.Render(toggleMark + " Replace")

	var sb strings.Builder
	sb.WriteString(strings.Join(searchRows, "\n"))
	sb.WriteByte('\n')
	sb.WriteString(toggleLine)

	if d.replaceOpen {
		replaceBorder := sraBorderStyle
		if d.focus == sraFocusReplace {
			replaceBorder = sraBorderFocusStyle
		}
		replaceBox := replaceBorder.Width(innerW).Render(d.replaceInput.View())
		replaceRows := strings.Split(replaceBox, "\n")

		allStyle := sraBorderStyle
		allLabel := " All "
		if d.focus == sraFocusAll {
			allStyle = sraBorderFocusStyle
		}
		allBox := allStyle.Render(allLabel)
		allRows := strings.Split(allBox, "\n")
		for i := range replaceRows {
			if i < len(allRows) {
				replaceRows[i] += "  " + allRows[i]
			}
		}
		sb.WriteByte('\n')
		sb.WriteString(strings.Join(replaceRows, "\n"))
	}

	sb.WriteByte('\n')
	switch {
	case d.confirmingAll:
		sb.WriteString(sraLabelFocusStyle.Render(fmt.Sprintf("Replace %d matches across the workspace? (y/n)", len(d.results))))
	case d.applyMsg != "":
		sb.WriteString(sraDimStyle.Render(d.applyMsg))
	case d.errMsg != "":
		sb.WriteString(sraErrStyle.Render("Error: " + d.errMsg))
	case d.searching:
		sb.WriteString(d.spinner.View() + " Searching…")
	case d.searched:
		sb.WriteString(sraDimStyle.Render(fmt.Sprintf("%d matches — enter to apply, tab to move, esc to close", len(d.results))))
	default:
		sb.WriteString(sraDimStyle.Render("enter to search"))
	}

	if len(d.results) > 0 {
		sb.WriteByte('\n')
		sb.WriteString(sraBorderStyle.Width(innerW).Render(d.viewport.View()))
	}

	return sraDialogBorderStyle.Render(sb.String())
}
