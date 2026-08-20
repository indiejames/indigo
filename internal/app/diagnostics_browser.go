package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/indiejames/indigo/internal/client"
)

// diagSeverityLabel maps an LSP-style severity (1=error,
// 2=warning, other=info) to a short display label — mirrors the status
// bar's E/W/I grouping (internal/client/render_view.go renderStatusBar).
func diagSeverityLabel(sev uint8) string {
	switch sev {
	case 1:
		return "E"
	case 2:
		return "W"
	default:
		return "I"
	}
}

// diagBrowser is the workspace diagnostic browser popup, opened via
// :diagnostics/:diag. Mirrors grepPicker's shape closely — same async,
// sequence-guarded fetch into a scrollable list, Enter-to-jump.
type diagBrowser struct {
	workDir   string
	items     []client.ClientWorkspaceDiag
	truncated bool
	cursor    int
	width     int
	height    int
	loading   bool
	errMsg    string
	seq       int // request sequence this browser is waiting on; see diagBrowserResultsMsg
}

// diagBrowserResultsMsg delivers async workspace diagnostics back to the
// App. seq guards against a slower, older request's results overwriting a
// newer one's, same as grepResultsMsg.
type diagBrowserResultsMsg struct {
	seq    int
	result client.WorkspaceDiagnosticsResult
	err    error
}

// diagBrowserPickedMsg is sent when the user confirms a result.
type diagBrowserPickedMsg struct {
	absPath string
	line    int // 0-based
	col     int // 0-based
}

// diagBrowserCancelledMsg is sent when the user presses Esc.
type diagBrowserCancelledMsg struct{}

func (db *diagBrowser) moveUp() {
	if db.cursor > 0 {
		db.cursor--
	}
}

func (db *diagBrowser) moveDown() {
	if db.cursor < len(db.items)-1 {
		db.cursor++
	}
}

// View renders the diagnostic browser as a full-screen overlay, matching
// the style of the workspace search picker (grepPicker.View).
func (db *diagBrowser) View() string {
	const chrome = 5
	innerW := db.width - 4
	maxItems := db.height - chrome - 2
	if maxItems < 1 {
		maxItems = 1
	}

	start := 0
	if db.cursor >= maxItems {
		start = db.cursor - maxItems + 1
	}
	end := start + maxItems
	if end > len(db.items) {
		end = len(db.items)
	}

	pad := strings.Repeat(" ", innerW)
	clamp := func(s string) string {
		r := []rune(s)
		if len(r) > innerW {
			return string(r[:innerW-1]) + "…"
		}
		return s + strings.Repeat(" ", innerW-len(r))
	}

	var sb strings.Builder

	var title string
	switch {
	case db.errMsg != "":
		title = "  Diagnostics  [error]"
	case db.loading:
		title = "  Diagnostics  [loading…]"
	case db.truncated:
		title = fmt.Sprintf("  Diagnostics  [%d+ results]", len(db.items))
	default:
		title = fmt.Sprintf("  Diagnostics  [%d results]", len(db.items))
	}
	sb.WriteString(pickerTitleStyle.Render(clamp(title)))
	sb.WriteByte('\n')

	hint := clamp("  ↑↓/jk navigate  Enter open  Esc cancel")
	sb.WriteString(pickerQueryStyle.Render(hint))
	sb.WriteByte('\n')

	sb.WriteString(pickerItemStyle.Render(pad))
	sb.WriteByte('\n')

	switch {
	case db.errMsg != "":
		sb.WriteString(pickerItemStyle.Render(clamp("  Error: " + db.errMsg)))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	case db.loading:
		sb.WriteString(pickerItemStyle.Render(clamp("  Loading…")))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	case len(db.items) == 0:
		sb.WriteString(pickerItemStyle.Render(clamp("  No diagnostics in any open buffer")))
		sb.WriteByte('\n')
		for i := 1; i < maxItems; i++ {
			sb.WriteString(pickerItemStyle.Render(pad))
			sb.WriteByte('\n')
		}
	default:
		for i := start; i < end; i++ {
			it := db.items[i]
			line := diagResultLine(db.workDir, it, innerW-2)
			if i == db.cursor {
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

	var status string
	if !db.loading && db.errMsg == "" && len(db.items) > 0 {
		status = fmt.Sprintf("  %d / %d", db.cursor+1, len(db.items))
	}
	sb.WriteString(pickerItemStyle.Render(clamp(status)))

	body := sb.String()

	boxW := innerW + 4
	boxH := maxItems + chrome
	box := pickerBorderStyle.Width(innerW).Height(boxH - 2).Render(body)

	col := max(0, (db.width-boxW)/2)
	row := max(0, (db.height-boxH)/2)

	var out strings.Builder
	blank := strings.Repeat(" ", db.width)
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

// diagDisplayPath returns a workspace-relative path for display, falling
// back to the base name if Rel fails (e.g. a path outside workDir).
func diagDisplayPath(workDir, path string) string {
	if workDir != "" && path != "" {
		if rel, err := filepath.Rel(workDir, path); err == nil {
			return rel
		}
	}
	return filepath.Base(path)
}

// diagResultLine formats one diagnostic as "[E] path:line: message",
// truncated to maxW runes.
func diagResultLine(workDir string, it client.ClientWorkspaceDiag, maxW int) string {
	rel := diagDisplayPath(workDir, it.Path)
	prefix := fmt.Sprintf("[%s] %s:%d:", diagSeverityLabel(it.Severity), rel, it.Line+1)
	full := prefix + " " + it.Message
	runes := []rune(full)
	if len(runes) > maxW {
		return string(runes[:maxW-1]) + "…"
	}
	return full
}
