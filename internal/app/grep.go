package app

import (
	"fmt"
	"strings"

	"github.com/indiejames/indigo/internal/workspacefs"

	"charm.land/lipgloss/v2"
)

// ---- grepPicker UI ----

type grepPicker struct {
	workDir   string
	pattern   string
	include   string // optional include glob filter (e.g. "*.go", "src/")
	exclude   string // optional exclude glob filter (e.g. "vendor/")
	results   []GrepResult
	cursor    int
	width     int
	height    int
	searching bool
	errMsg    string
	seq       int // request sequence this picker is waiting on; see grepResultsMsg
}

// grepResultsMsg delivers async search results back to the App. seq is the
// App's grepSeq counter value at the time the request was issued — a newer
// grep bumps the counter, so a slower older request's results (which could
// otherwise arrive after and overwrite a newer one's) are identifiable and
// discarded instead of applied.
type grepResultsMsg struct {
	seq     int
	results []GrepResult
	err     error
}

// grepPickedMsg is sent when the user confirms a result.
type grepPickedMsg struct {
	absPath  string
	line     int // 0-based
	col      int // 0-based column of match start
	matchLen int // rune length of match
}

// grepCancelledMsg is sent when the user presses Esc.
type grepCancelledMsg struct{}

func (gp *grepPicker) moveUp() {
	if gp.cursor > 0 {
		gp.cursor--
	}
}

func (gp *grepPicker) moveDown() {
	if gp.cursor < len(gp.results)-1 {
		gp.cursor++
	}
}

// View renders the grep picker as a full-screen overlay, matching the style
// of the file picker.
func (gp *grepPicker) View() string {
	const chrome = 5
	innerW := gp.width - 4
	maxItems := gp.height - chrome - 2
	if maxItems < 1 {
		maxItems = 1
	}

	start := 0
	if gp.cursor >= maxItems {
		start = gp.cursor - maxItems + 1
	}
	end := start + maxItems
	if end > len(gp.results) {
		end = len(gp.results)
	}

	pad := strings.Repeat(" ", innerW)
	// ANSI-aware (via lipgloss.Width/ansiTruncate, not a raw rune count):
	// grepResultLine can embed a bold-preserving escape sequence around the
	// matched text (see boldPreserving in searchreplace.go), which a plain
	// rune-count clamp would misjudge as extra visible width and truncate
	// or pad incorrectly.
	clamp := func(s string) string {
		w := lipgloss.Width(s)
		if w > innerW {
			return ansiTruncate(s, innerW)
		}
		return s + strings.Repeat(" ", innerW-w)
	}

	var sb strings.Builder

	// Title
	subject := gp.pattern
	if gp.include != "" || gp.exclude != "" {
		var parts []string
		if gp.include != "" {
			parts = append(parts, "in:"+gp.include)
		}
		if gp.exclude != "" {
			parts = append(parts, "!"+gp.exclude)
		}
		subject = fmt.Sprintf("%s  %s", gp.pattern, strings.Join(parts, " "))
	}
	var title string
	switch {
	case gp.errMsg != "":
		title = fmt.Sprintf("  Search: %s  [error]", subject)
	case gp.searching:
		title = fmt.Sprintf("  Search: %s  [searching…]", subject)
	case len(gp.results) >= workspacefs.MaxResults:
		title = fmt.Sprintf("  Search: %s  [%d+ results]", subject, workspacefs.MaxResults)
	default:
		title = fmt.Sprintf("  Search: %s  [%d results]", subject, len(gp.results))
	}
	sb.WriteString(pickerTitleStyle.Render(clamp(title)))
	sb.WriteByte('\n')

	hint := clamp("  ↑↓/jk navigate  Enter open  Esc cancel")
	sb.WriteString(pickerQueryStyle.Render(hint))
	sb.WriteByte('\n')

	sb.WriteString(pickerItemStyle.Render(pad))
	sb.WriteByte('\n')

	switch {
	case gp.errMsg != "":
		sb.WriteString(pickerItemStyle.Render(clamp("  Error: " + gp.errMsg)))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	case gp.searching:
		sb.WriteString(pickerItemStyle.Render(clamp("  Searching…")))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	default:
		for i := start; i < end; i++ {
			r := gp.results[i]
			line := grepResultLine(r, innerW-2)
			if i == gp.cursor {
				sb.WriteString(pickerSelStyle.Render(clamp("  " + line)))
			} else {
				sb.WriteString(pickerItemStyle.Render(clamp("  " + line)))
			}
			sb.WriteByte('\n')
		}
		for i := end - start; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	}

	// Bottom status: count of visible vs total.
	var status string
	if !gp.searching && gp.errMsg == "" && len(gp.results) > 0 {
		status = fmt.Sprintf("  %d / %d", gp.cursor+1, len(gp.results))
	}
	sb.WriteString(pickerItemStyle.Render(clamp(status)))

	body := sb.String()

	boxW := innerW + 4
	boxH := maxItems + chrome
	box := pickerBorderStyle.Width(innerW).Height(boxH - 2).Render(body)

	col := max(0, (gp.width-boxW)/2)
	row := max(0, (gp.height-boxH)/2)

	var out strings.Builder
	blank := strings.Repeat(" ", gp.width)
	for i := 0; i < row; i++ {
		out.WriteString(blank)
		out.WriteByte('\n')
	}
	for _, bline := range strings.Split(box, "\n") {
		out.WriteString(strings.Repeat(" ", col))
		out.WriteString(bline)
		out.WriteByte('\n')
	}
	return out.String()
}

// grepResultLine formats one result as "path:line: content", windowed to at
// most maxW runes so the match stays visible: whatever width remains once
// the match itself is reserved is split (via splitContext, shared with the
// search/replace dialog's results list) between context before and after
// the match, as a best-effort centering that degrades gracefully near
// either end of the line.
func grepResultLine(r GrepResult, maxW int) string {
	label := fmt.Sprintf("%s:%d: ", r.RelPath, r.Line+1)
	content := strings.TrimLeft(r.LineText, " \t")
	trimmedCount := len([]rune(r.LineText)) - len([]rune(content))
	matchStart := max(r.Col-trimmedCount, 0)
	matchEnd := max(matchStart+r.MatchLen, matchStart)

	// avail and all budget math below are in terminal cells (lipgloss.Width),
	// not rune counts — a wide rune (e.g. CJK) occupies 2 cells, so a
	// rune-count budget would let such a line silently render wider than
	// maxW.
	avail := maxW - lipgloss.Width(label)
	if avail < 1 {
		return fitSide(label+content, maxW, false)
	}

	cr := []rune(content)
	if matchEnd > len(cr) {
		matchEnd = len(cr)
	}
	if matchStart > matchEnd {
		matchStart = matchEnd
	}
	before := string(cr[:matchStart])
	match := string(cr[matchStart:matchEnd])
	after := string(cr[matchEnd:])

	beforeShown, afterShown := splitContext(before, after, avail-lipgloss.Width(match))
	line := label + beforeShown + match + afterShown
	// The match itself may be wider than avail (rare); hard-clip as a last
	// resort rather than overflowing the caller's width budget. This check
	// (and everything above it) works in plain text, so it must run before
	// the match is styled below — bold's escape codes aren't real display
	// width but would still throw off a width measurement.
	if lipgloss.Width(line) > maxW {
		return fitSide(line, maxW, false)
	}

	// boldPreserving (not a plain lipgloss Render) because this row is
	// about to be wrapped in pickerItemStyle/pickerSelStyle, both of which
	// set a background — a full-reset style Render nested inside would cut
	// a visible gap in it.
	return label + beforeShown + boldPreserving(match) + afterShown
}
