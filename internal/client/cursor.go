package client

import (
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Cursor rendering.
//
// indigo draws the primary cursor with the terminal's *own* cursor, placed
// via Bubble Tea v2's View.Cursor. Before v2 there was no way to position or
// shape the real cursor, so indigo painted a reverse-video character cell
// instead and kept the terminal cursor hidden. That worked for "block" and
// could fake "underline", but it could never produce a bar, which sits
// between cells rather than on one.
//
// Insert mode is the exception and still paints a cell, because it needs a
// *color* and terminal cursor color doesn't survive a multiplexer — see
// paintsBufferCursor for the measurement.
//
// Extra cursors in a multi-cursor selection are still painted as cells
// (buildExtraCursorOverlays in multicursor.go) — a terminal has exactly one
// real cursor, so only the primary one can use it.

// insertBlinkHalfPeriod is how long the painted insert-mode cursor stays on,
// then off. Insert mode can't use the terminal's own blink (it isn't using
// the terminal's cursor at all — see paintsBufferCursor), so the blink is
// done by simply not painting the cell on alternate phases.
//
// This costs nothing extra to animate: the client already ticks every 120ms
// to poll the server, Bubble Tea renders after every message, and the
// renderer diffs — so a blink rewrites exactly one cell on a frame that was
// being produced anyway. 480ms is four ticks, so each phase lasts a whole
// number of frames and the blink stays even instead of jittering by up to a
// tick. It's also close to the ~530ms terminals themselves use.
const insertBlinkHalfPeriod = 480 * time.Millisecond

// nowFunc is a seam for tests to pin the blink phase, in the same spirit as
// clipboardWriter in clipboard.go.
var nowFunc = time.Now

// insertCursorOn reports whether the painted insert cursor is in its visible
// phase. Always true when blinking is off.
func (m Model) insertCursorOn() bool {
	if !m.cursorBlinks() {
		return true
	}
	return (nowFunc().UnixNano()/int64(insertBlinkHalfPeriod))%2 == 0
}

// paintsBufferCursor reports whether the buffer cursor is drawn as a styled
// character cell instead of by the terminal. True in every mode but normal —
// except while a blinking insert cursor is in its off phase.
//
// Insert mode paints because that's the only way to color the cursor
// reliably. The color would otherwise be OSC 12, which multiplexers can
// swallow: measured directly in this project's setup, zellij 0.45.1 drops
// OSC 12 entirely (it has passthrough settings for OSC 52/133/8 but none for
// 12), so a themed insert cursor simply never changed color there. Cursor
// *shape* is DECSCUSR and does survive zellij, which is why normal mode can
// still use the real cursor. The trade is that insert mode gives up the
// configured *shape* — indigo is drawing a cell, and a cell can't be a bar.
// Blink survives, done in software above.
//
// Command and search mode paint for a different reason: the real cursor has
// moved to the prompt on the bottom line, so the painted cell is all that
// still marks the position in the buffer.
func (m Model) paintsBufferCursor() bool {
	switch m.mode {
	case ModeNormal:
		return false
	case ModeInsert:
		return m.insertCursorOn()
	default: // command, search
		return true
	}
}

// teaCursorShape maps the configured cursor_shape onto a terminal cursor
// shape. Any unrecognized value falls back to a block, matching how
// cursor_column_style treats an unknown value (see config.Config).
func (m Model) teaCursorShape() tea.CursorShape {
	if m.cfg == nil {
		return tea.CursorBlock
	}
	switch m.cfg.CursorShape {
	case "underline":
		return tea.CursorUnderline
	case "bar":
		return tea.CursorBar
	default:
		return tea.CursorBlock
	}
}

// cursorBlinks reports whether the cursor should blink. Off unless the user
// asked for it.
func (m Model) cursorBlinks() bool {
	return m.cfg != nil && m.cfg.CursorBlink
}

// newCursor builds the View.Cursor for a given screen position, applying the
// configured shape and blink.
//
// Color is deliberately left unset, so the terminal's own cursor color
// applies. The only mode that wants a distinct cursor color is insert, and
// insert paints a cell instead of using the terminal cursor at all (see
// paintsBufferCursor) — so there is no case left where setting Color here
// would do something a user can rely on.
func (m Model) newCursor(x, y int) *tea.Cursor {
	c := tea.NewCursor(x, y)
	c.Shape = m.teaCursorShape()
	c.Blink = m.cursorBlinks()
	return c
}

// bufferCursorPos returns the primary cursor's position within this model's
// frame, in screen cells: x accounts for the line-number gutter and for tab
// expansion, y for scrolling and soft-wrapped rows.
//
// The two-step row lookup mirrors the completion popup's (render_view.go):
// screenRowOf finds the row from the already-built layout, and
// cursorVisualRowFromTop covers the case where the cursor's line isn't in
// that layout. ok is false when the cursor isn't on screen at all, in which
// case no cursor should be shown.
func (m Model) bufferCursorPos(layout []layoutEntry, cw, vis int) (x, y int, ok bool) {
	visCol := m.cursor.Col
	if line := m.buf.Line(m.cursor.Line); len(line) > 0 {
		if _, colMap := expandTabsRemap([]rune(line)); m.cursor.Col < len(colMap) {
			visCol = colMap[m.cursor.Col]
		}
	}

	row := screenRowOf(layout, m.cursor.Line, visCol, cw)
	if row < 0 {
		row = m.cursorVisualRowFromTop(cw)
	}
	if row < 0 || row >= vis {
		return 0, 0, false
	}

	// A soft-wrapped row restarts at the gutter, so the column within the row
	// is the visual column modulo the content width.
	col := visCol
	if cw > 0 {
		col %= cw
	}
	x = m.gutterWidth() + col
	if x >= m.width {
		return 0, 0, false
	}
	return x, row, true
}

// promptCursorX returns the cursor's column on the command/search prompt
// line, which is drawn at the very end of the frame (row == vis). It mirrors
// the truncation renderStatusBar applies so the cursor lands on the same cell
// the prompt's trailing space occupies.
func promptCursorX(prompt string, width int) int {
	runes := []rune(prompt)
	if maxW := width - 1; maxW > 0 && len(runes) > maxW {
		runes = runes[len(runes)-maxW:]
	}
	return lipgloss.Width(string(runes))
}
