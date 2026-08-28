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
	"github.com/mattn/go-runewidth"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
)

// sraFocus identifies which control in the search/replace dialog has focus.
type sraFocus int

const (
	sraFocusSearch sraFocus = iota
	sraFocusCase
	sraFocusRegex
	sraFocusFilterToggle
	sraFocusInclude
	sraFocusExclude
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
	includeInput  textinput.Model
	excludeInput  textinput.Model
	caseSensitive bool
	useRegex      bool
	replaceOpen   bool
	filterOpen    bool
	focus         sraFocus

	searching bool
	searched  bool
	spinner   spinner.Model
	results   []GrepResult
	cursor    int
	viewport  viewport.Model
	// resultsMaxContentW is the natural (untruncated) width of the widest
	// current result line, used by resultsW to size the results box to fit
	// its content instead of always claiming the maximum available width.
	resultsMaxContentW int

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

	ii := textinput.New()
	ii.Placeholder = "Include (e.g. *.go, src/**)"
	ii.Prompt = ""

	ei := textinput.New()
	ei.Placeholder = "Exclude (e.g. vendor/, *_test.go)"
	ei.Prompt = ""

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	d := &searchReplaceDialog{
		workDir:      workDir,
		width:        w,
		height:       h,
		searchInput:  si,
		replaceInput: ri,
		includeInput: ii,
		excludeInput: ei,
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

// dialogResultsW is the width of the results list and its viewport, kept
// separate from dialogInnerW (which sizes the search/replace/filter input
// boxes) so the results — where a long file path can otherwise crowd out
// the matching text — can use most of the available terminal width instead
// of being capped at the same narrow width as a single-line text input.
func dialogResultsW(termW int) int {
	return max(termW-12, 24)
}

// resultsW is the width of the results list and its viewport: it shrinks to
// fit the widest current result line rather than always claiming
// dialogResultsW's full terminal-width-based cap, but never shrinks below
// the search/replace input boxes' width (dialogInnerW) and never exceeds
// that cap either.
func (d *searchReplaceDialog) resultsW() int {
	return min(max(d.resultsMaxContentW, dialogInnerW(d.width)), dialogResultsW(d.width))
}

// resultLineNaturalWidth returns the width renderResultLine's line would be
// for r if nothing were truncated, used to size the results box to fit
// content instead of always claiming the maximum available width.
func (d *searchReplaceDialog) resultLineNaturalWidth(r GrepResult) int {
	oldText := oldTextOf(r)
	newText := d.replaceInput.Value()
	loc := fmt.Sprintf("%s:%d:", r.RelPath, r.Line+1)
	prefix := strings.TrimLeft(r.LineText[:byteOffsetOfRune(r.LineText, r.Col)], " \t")
	suffix := suffixOf(r)

	tailPlain := oldText
	if d.replaceOpen && newText != "" {
		tailPlain = oldText + " → " + newText
	}

	return lipgloss.Width(loc) + 1 + lipgloss.Width(prefix) + lipgloss.Width(tailPlain) + lipgloss.Width(suffix)
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
	order := []sraFocus{sraFocusSearch, sraFocusCase, sraFocusRegex, sraFocusFilterToggle}
	if d.filterOpen {
		order = append(order, sraFocusInclude, sraFocusExclude)
	}
	order = append(order, sraFocusToggle)
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
	if f == sraFocusInclude {
		d.includeInput.Focus()
	} else {
		d.includeInput.Blur()
	}
	if f == sraFocusExclude {
		d.excludeInput.Focus()
	} else {
		d.excludeInput.Blur()
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
	include := d.includeInput.Value()
	exclude := d.excludeInput.Value()
	searchCmd := func() tea.Msg {
		results, err := searchWorkspaceExplicit(workDir, pattern, include, exclude, caseSensitive, useRegex)
		return sraResultsMsg{results: results, err: err}
	}
	return tea.Batch(searchCmd, d.spinner.Tick)
}

// matchBounds returns the clamped [start,end) rune range r.Col/r.MatchLen
// describe within r.LineText, or (0,0) if r.Col is out of range.
func matchBounds(r GrepResult) (start, end int) {
	lr := []rune(r.LineText)
	end = min(r.Col+r.MatchLen, len(lr))
	if r.Col < 0 || r.Col > end {
		return 0, 0
	}
	return r.Col, end
}

// oldTextOf returns the exact matched runes for r, used both for the on-screen
// diff and as the server-verified oldText of a WorkspaceEdit.
func oldTextOf(r GrepResult) string {
	lr := []rune(r.LineText)
	start, end := matchBounds(r)
	return string(lr[start:end])
}

// suffixOf returns the line text immediately following r's match, used to
// show trailing context in the results list.
func suffixOf(r GrepResult) string {
	lr := []rune(r.LineText)
	_, end := matchBounds(r)
	return string(lr[end:])
}

func (d *searchReplaceDialog) refreshResultsView() {
	maxW := 0
	for _, r := range d.results {
		if w := d.resultLineNaturalWidth(r); w > maxW {
			maxW = w
		}
	}
	d.resultsMaxContentW = maxW
	d.viewport.Width = d.resultsW()

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
		bufID, content, version, fromRecovery, generation, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return sraSingleResultMsg{err: err}
		}
		m := client.New(rpc, bufID, content, version, absPath, workDir, cfg, fromRecovery, generation)
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
	sraOldTextStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#CC6666")).Strikethrough(true).Bold(true)
	sraNewTextStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#66CC88")).Bold(true)
	sraMatchStyle       = lipgloss.NewStyle().Bold(true)
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
	suffix := suffixOf(r)
	showDiff := d.replaceOpen && newText != ""

	w := d.resultsW()

	tailPlain := oldText
	if showDiff {
		tailPlain = oldText + " → " + newText
	}

	// Keep the match (and its replacement, in diff mode) fully visible,
	// then use whatever width remains to show as much surrounding line
	// context as fits, split ~evenly between before and after the match —
	// any unused share on a shorter side goes to the other — as a
	// best-effort centering that degrades gracefully near either end of
	// the line.
	avail := w - lipgloss.Width(loc) - 1 - lipgloss.Width(tailPlain)
	prefixShown, suffixShown := splitContext(prefix, suffix, avail)

	if selected {
		// Under the selection background, the match is bolded with
		// boldPreserving rather than a full-reset lipgloss Render — the
		// latter would cut a visible gap in sraSelStyle's background.
		diffTail := ""
		if showDiff {
			diffTail = " → " + newText
		}
		plain := fmt.Sprintf("%s %s%s%s%s", loc, prefixShown, boldPreserving(oldText), diffTail, suffixShown)
		plain = ansiTruncate(plain, w)
		pad := max(0, w-lipgloss.Width(plain))
		return sraSelStyle.Render(plain + strings.Repeat(" ", pad))
	}

	matchRendered := sraMatchStyle.Render(oldText)
	if showDiff {
		matchRendered = sraOldTextStyle.Render(oldText) + " → " + sraNewTextStyle.Render(newText)
	}
	line := fmt.Sprintf("%s %s%s%s", loc, prefixShown, matchRendered, suffixShown)
	if lipgloss.Width(line) > w {
		line = ansiTruncate(line, w)
	}
	return line
}

// splitContext divides avail terminal cells of width between before and
// after — context shown immediately before and after a match — as evenly as
// possible, reallocating any budget a shorter side can't use to the other
// side. before is truncated from its start (kept text anchored to the
// match, leading ellipsis); after is truncated from its end (kept text
// anchored to the match, trailing ellipsis). Budgets and comparisons use
// cell width (runewidth.StringWidth), not rune counts, since a wide rune
// (e.g. CJK) occupies 2 terminal cells — a rune-count budget would let such
// text silently render wider than avail.
func splitContext(before, after string, avail int) (string, string) {
	if avail < 0 {
		avail = 0
	}
	beforeW := runewidth.StringWidth(before)
	afterW := runewidth.StringWidth(after)

	beforeBudget := avail / 2
	afterBudget := avail - beforeBudget
	if beforeW < beforeBudget {
		afterBudget += beforeBudget - beforeW
		beforeBudget = beforeW
	}
	if afterW < afterBudget {
		extra := afterBudget - afterW
		afterBudget = afterW
		beforeBudget = min(beforeBudget+extra, beforeW)
	}

	return fitSide(before, beforeBudget, true), fitSide(after, afterBudget, false)
}

// fitSide returns a prefix/suffix of s whose cell width (not rune count) is
// at most budget, so a wide rune at the cut boundary can't push the result
// past budget columns. When s must be cut, keepEnd chooses which end is
// preserved (true keeps the tail, with a leading ellipsis — for text
// immediately before a match; false keeps the head, with a trailing
// ellipsis — for text immediately after one).
func fitSide(s string, budget int, keepEnd bool) string {
	if budget <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= budget {
		return s
	}
	if budget == 1 {
		return "…"
	}
	target := budget - 1 // reserve 1 cell for the ellipsis
	r := []rune(s)
	if keepEnd {
		w, i := 0, len(r)
		for i > 0 {
			rw := runewidth.RuneWidth(r[i-1])
			if w+rw > target {
				break
			}
			w += rw
			i--
		}
		return "…" + string(r[i:])
	}
	w, i := 0, 0
	for i < len(r) {
		rw := runewidth.RuneWidth(r[i])
		if w+rw > target {
			break
		}
		w += rw
		i++
	}
	return string(r[:i]) + "…"
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

// boldPreserving wraps s in a bare SGR bold-on/bold-off pair instead of a
// full lipgloss style Render (which appends a full reset). It's used to
// highlight a match nested inside an already-styled line — e.g. a selected
// row's background, or the grep picker's per-row background — where a full
// reset mid-string would cut a visible gap in that outer styling; SGR 22
// (bold off) clears only the bold attribute, leaving any active
// background/foreground color from the outer style untouched.
func boldPreserving(s string) string {
	if s == "" {
		return s
	}
	return "\x1b[1m" + s + "\x1b[22m"
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

	filterMark := "▸"
	if d.filterOpen {
		filterMark = "▾"
	}
	filterStyle := sraLabelStyle
	if d.focus == sraFocusFilterToggle {
		filterStyle = sraLabelFocusStyle
	}
	filterLine := filterStyle.Render(filterMark + " Filter")

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
	sb.WriteString(filterLine)

	if d.filterOpen {
		includeBorder := sraBorderStyle
		if d.focus == sraFocusInclude {
			includeBorder = sraBorderFocusStyle
		}
		excludeBorder := sraBorderStyle
		if d.focus == sraFocusExclude {
			excludeBorder = sraBorderFocusStyle
		}
		sb.WriteByte('\n')
		sb.WriteString(includeBorder.Width(innerW).Render(d.includeInput.View()))
		sb.WriteByte('\n')
		sb.WriteString(excludeBorder.Width(innerW).Render(d.excludeInput.View()))
	}

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
		sb.WriteString(sraBorderStyle.Width(d.resultsW()).Render(d.viewport.View()))
	}

	return sraDialogBorderStyle.Render(sb.String())
}
