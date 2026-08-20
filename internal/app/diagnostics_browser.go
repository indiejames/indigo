package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// fetchDiagBrowserResults fetches the current workspace diagnostics for
// a.diagBrowser, guarded by seq (see diagBrowserResultsMsg).
func (a App) fetchDiagBrowserResults(seq int) tea.Cmd {
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := rpc.GetWorkspaceDiagnostics(ctx)
		return diagBrowserResultsMsg{seq: seq, result: result, err: err}
	}
}

// rescanDiagBrowser triggers a fresh workspace lint scan (fire-and-forget
// on the server, see RPC.RescanWorkspaceDiagnostics) and then re-fetches
// the diagnostic list — the scan itself may still be running when the
// re-fetch lands, in which case it just shows the same results again until
// the user presses "r" once more, or reopens the browser, after the scan
// has actually finished. Returns the updated App (with diagSeq bumped) as
// well as the fetch command: a.diagSeq is a plain field, not a pointer, so
// the increment below only sticks if the caller propagates this returned
// App back into the model — unlike a.diagBrowser.seq, which is reachable
// through the *diagBrowser pointer and so mutates in place either way.
func (a App) rescanDiagBrowser() (App, tea.Cmd) {
	a.diagSeq++
	seq := a.diagSeq
	a.diagBrowser.seq = seq
	a.diagBrowser.loading = true
	a.diagBrowser.errMsg = ""
	rpc := a.rpc
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rpc.RescanWorkspaceDiagnostics(ctx); err != nil {
			return diagBrowserResultsMsg{seq: seq, err: err}
		}
		result, err := rpc.GetWorkspaceDiagnostics(ctx)
		return diagBrowserResultsMsg{seq: seq, result: result, err: err}
	}
}

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
	innerW := max(1, db.width-4)
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

	hint := clamp("  ↑↓/jk navigate  Enter open  r rescan  Esc cancel")
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
		sb.WriteString(pickerItemStyle.Render(clamp("  No diagnostics found")))
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
	if maxW <= 0 {
		return ""
	}
	rel := diagDisplayPath(workDir, it.Path)
	prefix := fmt.Sprintf("[%s] %s:%d:", diagSeverityLabel(it.Severity), rel, it.Line+1)
	full := prefix + " " + it.Message
	runes := []rune(full)
	if len(runes) > maxW {
		if maxW == 1 {
			return "…"
		}
		return string(runes[:maxW-1]) + "…"
	}
	return full
}
