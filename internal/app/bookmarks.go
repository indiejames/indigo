package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// bookmark records a user-set position that persists across edits.
type bookmark struct {
	filePath         string
	line             int
	col              int
	note             string
	marker           string // glyph shown in the left gutter, e.g. "▶"
	active           bool
	deactivatedDepth int // UndoDepth of the delete that suspended this bookmark
}

// bookmarkPicker is the popup that lets the user navigate and jump to bookmarks.
type bookmarkPicker struct {
	items  []bookmarkPickerItem
	cursor int
	width  int
	height int
}

type bookmarkPickerItem struct {
	bmarkIdx int
	filePath string
	line     int
	col      int
	note     string
}

type bmarkPickedMsg struct{ bmarkIdx int }
type bmarkPickerCancelledMsg struct{}
type bmarkDeletedMsg struct{ bmarkIdx int }

func newBookmarkPicker(bmarks []bookmark, w, h int) *bookmarkPicker {
	var items []bookmarkPickerItem
	for i, b := range bmarks {
		if b.active {
			items = append(items, bookmarkPickerItem{
				bmarkIdx: i,
				filePath: b.filePath,
				line:     b.line,
				col:      b.col,
				note:     b.note,
			})
		}
	}
	return &bookmarkPicker{items: items, cursor: 0, width: w, height: h}
}

func (bp *bookmarkPicker) moveUp() {
	if bp.cursor > 0 {
		bp.cursor--
	}
}

func (bp *bookmarkPicker) moveDown() {
	if bp.cursor < len(bp.items)-1 {
		bp.cursor++
	}
}

func (bp *bookmarkPicker) selectedBmarkIdx() int {
	if len(bp.items) == 0 {
		return -1
	}
	return bp.items[bp.cursor].bmarkIdx
}

const bmarkPickerMaxVisible = 14

var (
	bmarkPickerBg = lipgloss.Color("#0F1B2D")

	bmarkPickerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#2255AA")).
				Background(bmarkPickerBg)

	bmarkPickerTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0A1222")).
				Foreground(lipgloss.Color("#88AAFF")).
				Bold(true).
				Padding(0, 1)

	bmarkPickerItemStyle = lipgloss.NewStyle().
				Background(bmarkPickerBg).
				Foreground(lipgloss.Color("#AABBCC")).
				Padding(0, 1)

	bmarkPickerSelStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#1A3060")).
				Foreground(lipgloss.Color("#FFFFFF")).
				Padding(0, 1)

	bmarkPickerHintStyle = lipgloss.NewStyle().
				Background(bmarkPickerBg).
				Foreground(lipgloss.Color("#445566")).
				Padding(0, 1)

	bmarkPickerLocStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#556688"))

	bmarkMarker = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5588FF")).
			Render("▶")
)

func (bp *bookmarkPicker) render() string {
	const hint = " [Enter=jump  r=rename  d=delete  Esc=close]"
	innerW := len(hint)
	for _, item := range bp.items {
		base := filepath.Base(item.filePath)
		loc := fmt.Sprintf(" %s:%d", base, item.line+1)
		primary := item.note
		if primary == "" {
			primary = base + fmt.Sprintf(":%d", item.line+1)
			loc = ""
		}
		n := 4 + len([]rune(primary)) + len(loc)
		if n > innerW {
			innerW = n
		}
	}
	innerW = min(innerW, bp.width*2/3)
	innerW = max(innerW, 44)

	vis := min(len(bp.items), bmarkPickerMaxVisible)
	start := max(0, min(bp.cursor-vis/2, len(bp.items)-vis))
	end := min(start+vis, len(bp.items))

	divStyle := lipgloss.NewStyle().
		Background(bmarkPickerBg).
		Foreground(lipgloss.Color("#223355"))

	var rows []string
	rows = append(rows, bmarkPickerTitleStyle.Width(innerW).Render("Bookmarks"))
	rows = append(rows, divStyle.Render(strings.Repeat("─", innerW)))
	rows = append(rows, bmarkPickerHintStyle.Width(innerW).Render(hint))
	rows = append(rows, divStyle.Render(strings.Repeat("─", innerW)))

	if start > 0 {
		rows = append(rows, bmarkPickerItemStyle.Width(innerW).Render("  ↑ more"))
	}

	for i := start; i < end; i++ {
		item := bp.items[i]
		base := filepath.Base(item.filePath)
		if base == "" || base == "." {
			base = "[no name]"
		}
		var primary, loc string
		if item.note != "" {
			primary = item.note
			loc = fmt.Sprintf(" %s:%d", base, item.line+1)
		} else {
			primary = base + fmt.Sprintf(":%d", item.line+1)
		}
		// Truncate primary if needed, leaving room for loc.
		avail := innerW - 4 - len(loc)
		if len([]rune(primary)) > avail && avail > 0 {
			primary = string([]rune(primary)[:max(0, avail-1)]) + "…"
		}

		if i == bp.cursor {
			locRendered := bmarkPickerLocStyle.Copy().Background(lipgloss.Color("#1A3060")).Render(loc)
			label := bmarkMarker + " " + primary + locRendered
			rows = append(rows, bmarkPickerSelStyle.Width(innerW).Render(label))
		} else {
			locRendered := bmarkPickerLocStyle.Render(loc)
			label := bmarkMarker + " " + primary + locRendered
			rows = append(rows, bmarkPickerItemStyle.Width(innerW).Render(label))
		}
	}

	if end < len(bp.items) {
		rows = append(rows, bmarkPickerItemStyle.Width(innerW).Render("  ↓ more"))
	}

	return bmarkPickerBorderStyle.Render(strings.Join(rows, "\n"))
}

// bookmarkNamePrompt is the inline overlay that collects a name when setting
// or renaming a bookmark.
type bookmarkNamePrompt struct {
	filePath  string
	line, col int
	marker    string
	name      string // typed so far
	renameIdx int    // ≥0 when renaming an existing bookmark; -1 when creating new
	width     int
	height    int
}

type bmarkNameConfirmedMsg struct {
	filePath  string
	line, col int
	marker    string
	name      string
	renameIdx int
}
type bmarkNameCancelledMsg struct{}
type bmarkRenameMsg struct{ bmarkIdx int }

func newBookmarkNamePrompt(filePath string, line, col int, marker string, w, h int) *bookmarkNamePrompt {
	return &bookmarkNamePrompt{
		filePath:  filePath,
		line:      line,
		col:       col,
		marker:    marker,
		renameIdx: -1,
		width:     w,
		height:    h,
	}
}

func newBookmarkRenamePrompt(b bookmark, idx, w, h int) *bookmarkNamePrompt {
	return &bookmarkNamePrompt{
		filePath:  b.filePath,
		line:      b.line,
		col:       b.col,
		marker:    b.marker,
		name:      b.note,
		renameIdx: idx,
		width:     w,
		height:    h,
	}
}

var (
	bmarkPromptBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#2255AA")).
				Background(lipgloss.Color("#0F1B2D"))

	bmarkPromptTitleStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0A1222")).
				Foreground(lipgloss.Color("#88AAFF")).
				Bold(true).
				Padding(0, 1)

	bmarkPromptInputStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#152035")).
				Foreground(lipgloss.Color("#DDEEFF")).
				Padding(0, 1)

	bmarkPromptHintStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#0F1B2D")).
				Foreground(lipgloss.Color("#445566")).
				Padding(0, 1)
)

func (p *bookmarkNamePrompt) render() string {
	innerW := max(40, min(60, p.width*2/3))

	title := "Set Bookmark Name"
	if p.renameIdx >= 0 {
		title = "Rename Bookmark"
	}
	cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#AACCFF")).Render("█")
	input := p.name + cursor
	hint := " [Enter=confirm  Esc=cancel  Backspace=delete]"

	rows := []string{
		bmarkPromptTitleStyle.Width(innerW).Render(title),
		bmarkPromptInputStyle.Width(innerW).Render(" " + input),
		bmarkPromptHintStyle.Width(innerW).Render(hint),
	}
	return bmarkPromptBorderStyle.Render(strings.Join(rows, "\n"))
}
