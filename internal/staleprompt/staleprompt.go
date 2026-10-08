// Package staleprompt is the "server is running an older build" popup shared
// by every kind of window: the editor (internal/app) and the debug window
// (internal/debugwin).
//
// The server reports at connect time whether it is running code that has since
// been replaced on disk (see internal/server/staleness.go). That used to be a
// line on stderr printed just before the TUI took the alternate screen, so it
// was only ever seen after quitting — too late to act on. Each window now shows
// this popup over its first frame instead, with Quit selected.
//
// A package of its own because the debug window must not import internal/app:
// that pulls in the editor and, through it, the tree-sitter cgo binding.
package staleprompt

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// Choice is the selected option.
type Choice int

const (
	Quit Choice = iota
	Continue
)

// Prompt is the popup's state. The zero value has Quit selected.
type Prompt struct {
	Sel Choice
}

// Outcome is what a key press decided.
type Outcome int

const (
	// Pending: the prompt stays up. Every key that is not an answer lands
	// here and is swallowed, so a key meant for the window behind it cannot
	// reach it.
	Pending Outcome = iota
	// Dismissed: carry on with the old server.
	Dismissed
	// QuitRequested: the window should quit.
	QuitRequested
)

// Key applies a key press, named as tea.KeyMsg.String() names it.
func (p Prompt) Key(key string) (Prompt, Outcome) {
	switch key {
	case "up", "k":
		p.Sel = Quit
	case "down", "j":
		p.Sel = Continue
	case "q":
		return p, QuitRequested
	case "esc", "c":
		return p, Dismissed
	case "enter":
		if p.Sel == Quit {
			return p, QuitRequested
		}
		return p, Dismissed
	}
	return p, Pending
}

// Render draws the popup for a terminal w columns wide. Quitting one window
// does not fix a stale server — it lives until its last client disconnects —
// so the text says every window has to go, which is the obvious next
// confusion otherwise.
func (p Prompt) Render(w int) string {
	innerW := 56
	if w > 0 && w-4 < innerW {
		innerW = w - 4
	}
	if innerW < 30 {
		innerW = 30
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FFAA44")).
		Background(lipgloss.Color("#1E2A38"))
	titleStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#0D1B2A")).
		Foreground(lipgloss.Color("#FFAA44")).
		Bold(true).
		Padding(0, 1)
	divStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#FFAA44"))
	bodyStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#DDDDDD")).
		Padding(0, 1)
	itemStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#AABBCC")).
		Padding(0, 1)
	selStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#2D5F8A")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1)

	style := func(c Choice, label string) string {
		if c == p.Sel {
			return selStyle.Width(innerW).Render(label)
		}
		return itemStyle.Width(innerW).Render(label)
	}

	rows := []string{
		titleStyle.Width(innerW).Render("⚠  Server is out of date"),
		divStyle.Render(strings.Repeat("─", innerW)),
		bodyStyle.Width(innerW).Render(
			"The indigo server for this workspace started before the " +
				"current indigo was installed, so recent changes are not " +
				"in effect. To pick up the new build, close every indigo " +
				"window on this workspace."),
		divStyle.Render(strings.Repeat("─", innerW)),
		style(Quit, "  Quit  (q)"),
		style(Continue, "  Continue anyway  (esc)"),
	}
	return borderStyle.Render(strings.Join(rows, "\n"))
}
