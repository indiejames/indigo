package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/theme"
)

func cursorTestModel(t *testing.T, content string, cfg *config.Config) Model {
	t.Helper()
	m := newTestModel(content)
	m.width, m.height = 40, 8
	m.mode = ModeNormal
	m.cfg = cfg
	return m
}

// TestCursorShapeMapping covers the config → terminal shape mapping,
// including "bar" — the shape that was impossible before v2, because a bar
// sits between cells and can't be drawn by restyling one.
func TestCursorShapeMapping(t *testing.T) {
	cases := []struct {
		shape string
		want  tea.CursorShape
	}{
		{"block", tea.CursorBlock},
		{"underline", tea.CursorUnderline},
		{"bar", tea.CursorBar},
		{"", tea.CursorBlock},          // unset falls back to block
		{"diamond", tea.CursorBlock},   // unrecognized falls back to block
		{"Underline", tea.CursorBlock}, // matching is exact, like cursor_column_style
	}
	for _, tc := range cases {
		m := cursorTestModel(t, "hello\n", &config.Config{CursorShape: tc.shape})
		if got := m.View().Cursor; got == nil {
			t.Errorf("cursor_shape=%q: View returned no cursor", tc.shape)
		} else if got.Shape != tc.want {
			t.Errorf("cursor_shape=%q: shape = %v, want %v", tc.shape, got.Shape, tc.want)
		}
	}
}

// TestCursorBlinkDefaultsOff pins the requirement that blinking is opt-in.
func TestCursorBlinkDefaultsOff(t *testing.T) {
	t.Run("absent from config", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{})
		if c := m.View().Cursor; c == nil || c.Blink {
			t.Errorf("blink = %v, want false by default", c != nil && c.Blink)
		}
	})

	t.Run("nil config", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", nil)
		if c := m.View().Cursor; c == nil || c.Blink {
			t.Errorf("blink with no config at all = %v, want false", c != nil && c.Blink)
		}
	})

	t.Run("opted in", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{CursorBlink: true})
		if c := m.View().Cursor; c == nil || !c.Blink {
			t.Error("cursor_blink=true did not reach the rendered cursor")
		}
	})
}

// TestInsertModePaintsGreenCellInsteadOfTerminalCursor covers the insert-mode
// cursor, which is deliberately NOT the terminal's own.
//
// The terminal cursor can't be colored reliably: its color is OSC 12, and
// zellij drops that sequence outright (measured — see paintsBufferCursor), so
// under a multiplexer a themed insert cursor never changed color at all.
// Painting the cell is the only mechanism that provably renders everywhere,
// so insert mode paints and hides the terminal cursor rather than drawing
// both.
func TestInsertModePaintsGreenCellInsteadOfTerminalCursor(t *testing.T) {
	applyDefaultDark()
	t.Cleanup(applyDefaultDark)

	m := cursorTestModel(t, "hello\n", &config.Config{})
	m.mode = ModeInsert
	v := m.View()

	if v.Cursor != nil {
		t.Errorf("insert mode still emits a terminal cursor at (%d,%d); it must be hidden so it "+
			"isn't drawn on top of the painted green cell", v.Cursor.X, v.Cursor.Y)
	}
	// Cursor is at line 0 col 0, so the painted cell is "hello"'s "h".
	if want := insertCursorStyle.Render("h"); !strings.Contains(v.Content, want) {
		t.Errorf("insert mode did not paint the cursor cell %q into the frame", want)
	}
}

// TestInsertPaintedCellFollowsTheme pins that the painted insert cursor takes
// its color from the theme's insert_cursor_bg, so changing the theme actually
// changes the green.
func TestInsertPaintedCellFollowsTheme(t *testing.T) {
	applyDefaultDark()
	t.Cleanup(applyDefaultDark)

	const themed = "#FF00FF"
	th := theme.Default()
	th.UI.InsertCursorBg = themed
	ApplyTheme(th)

	m := cursorTestModel(t, "hello\n", &config.Config{})
	m.mode = ModeInsert

	want := lipgloss.NewStyle().
		Background(lipgloss.Color(themed)).
		Foreground(lipgloss.Color("#000000")).
		Render("h")
	if got := m.View().Content; !strings.Contains(got, want) {
		t.Errorf("painted insert cursor did not pick up the theme's insert_cursor_bg %s", themed)
	}
}

// TestInsertCursorBlinksInSoftware covers the insert-mode blink. Insert mode
// paints a cell rather than using the terminal's cursor, so it can't use the
// terminal's own blink either — the cell is simply not painted on alternate
// phases. The client already renders every 120ms for its server poll, so
// this animates without adding a timer of its own.
func TestInsertCursorBlinksInSoftware(t *testing.T) {
	applyDefaultDark()
	t.Cleanup(applyDefaultDark)

	// Pin the clock so the phase is deterministic instead of depending on
	// when the test happens to run.
	var now time.Time
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = time.Now })

	// Epoch is an "on" phase; one half-period later is "off".
	on := time.Unix(0, 0)
	off := on.Add(insertBlinkHalfPeriod)

	cell := insertCursorStyle.Render("h")

	t.Run("blink enabled alternates", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{CursorBlink: true})
		m.mode = ModeInsert

		now = on
		if !strings.Contains(m.View().Content, cell) {
			t.Error("blink on-phase: cursor cell missing; it should be visible")
		}
		now = off
		if strings.Contains(m.View().Content, cell) {
			t.Error("blink off-phase: cursor cell still painted; it should be hidden")
		}
		now = off.Add(insertBlinkHalfPeriod)
		if !strings.Contains(m.View().Content, cell) {
			t.Error("cursor did not come back on for the next phase")
		}
	})

	t.Run("blink disabled stays solid", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{})
		m.mode = ModeInsert
		for _, now = range []time.Time{on, off} {
			if !strings.Contains(m.View().Content, cell) {
				t.Errorf("cursor_blink=false: cursor vanished at %v; it must stay solid", now)
			}
		}
	})

	t.Run("only insert blinks", func(t *testing.T) {
		// Command mode also paints a cell, but as a position marker while the
		// real cursor is on the prompt — it must not flicker.
		m := cursorTestModel(t, "hello\n", &config.Config{CursorBlink: true})
		m.mode = ModeCommand
		m.cmdBuf = "zzzznosuchcommand"
		now = off
		if !strings.Contains(m.View().Content, normalCursorStyle.Render("h")) {
			t.Error("command mode's position marker blinked; only insert mode should")
		}
	})
}

// TestNormalModeUsesTerminalCursorWithNoColor is the counterpart: normal mode
// does use the terminal cursor, and leaves its color alone so a cursor color
// configured in the terminal itself still applies.
func TestNormalModeUsesTerminalCursorWithNoColor(t *testing.T) {
	applyDefaultDark()
	t.Cleanup(applyDefaultDark)

	m := cursorTestModel(t, "hello\n", &config.Config{})
	c := m.View().Cursor
	if c == nil {
		t.Fatal("normal mode should use the terminal cursor")
	}
	if c.Color != nil {
		t.Errorf("normal mode set cursor color to %v; it should stay unset so the terminal's "+
			"own cursor color applies", c.Color)
	}
}

// TestCursorPositionTracksBufferCursor checks the screen position accounts
// for the line-number gutter and for moving down/right through the buffer.
// A wrong position is the most visible possible bug here: the cursor would
// sit somewhere other than where typing lands.
func TestCursorPositionTracksBufferCursor(t *testing.T) {
	m := cursorTestModel(t, "hello\nworld\n", &config.Config{})
	gutter := m.gutterWidth()

	origin := m.View().Cursor
	if origin == nil {
		t.Fatal("no cursor at buffer origin")
	}
	if origin.X != gutter || origin.Y != 0 {
		t.Errorf("cursor at line 0 col 0 = (%d,%d), want (%d,0) — X must clear the gutter",
			origin.X, origin.Y, gutter)
	}

	m.cursor.Line, m.cursor.Col = 1, 3
	moved := m.View().Cursor
	if moved == nil {
		t.Fatal("no cursor after moving")
	}
	if moved.X != gutter+3 || moved.Y != 1 {
		t.Errorf("cursor at line 1 col 3 = (%d,%d), want (%d,1)", moved.X, moved.Y, gutter+3)
	}
}

// TestCursorPositionExpandsTabs guards the tab case specifically: the cursor
// is placed in screen cells, so a leading tab has to advance X by the tab's
// display width, not by one.
func TestCursorPositionExpandsTabs(t *testing.T) {
	m := cursorTestModel(t, "\tx\n", &config.Config{})
	m.cursor.Line, m.cursor.Col = 0, 1 // just after the tab

	c := m.View().Cursor
	if c == nil {
		t.Fatal("no cursor")
	}
	if want := m.gutterWidth() + 1; c.X <= want {
		t.Errorf("cursor X after a tab = %d, want > %d: the tab was counted as a single "+
			"column instead of its display width", c.X, want)
	}
}

// TestCursorMovesToPromptInCommandAndSearchMode verifies the cursor follows
// the prompt to the bottom line, where those modes actually take input, and
// that the buffer keeps its painted cell there as the position reminder.
func TestCursorMovesToPromptInCommandAndSearchMode(t *testing.T) {
	promptRow := func(m Model) int { return m.visibleLines() }

	t.Run("command mode", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{})
		m.mode = ModeCommand
		m.cmdBuf = "wq"

		c := m.View().Cursor
		if c == nil {
			t.Fatal("no cursor in command mode")
		}
		if c.Y != promptRow(m) {
			t.Errorf("cursor row = %d, want %d (the prompt line)", c.Y, promptRow(m))
		}
		if want := len(":wq"); c.X != want {
			t.Errorf("cursor col = %d, want %d (just past \":wq\")", c.X, want)
		}
	})

	t.Run("search mode", func(t *testing.T) {
		m := cursorTestModel(t, "hello\n", &config.Config{})
		m.mode = ModeSearch
		m.searchQuery = "ell"

		c := m.View().Cursor
		if c == nil {
			t.Fatal("no cursor in search mode")
		}
		if c.Y != promptRow(m) {
			t.Errorf("cursor row = %d, want %d (the prompt line)", c.Y, promptRow(m))
		}
		if want := len("/ell"); c.X != want {
			t.Errorf("cursor col = %d, want %d (just past \"/ell\")", c.X, want)
		}
	})

	t.Run("buffer cursor stays painted", func(t *testing.T) {
		applyDefaultDark()
		t.Cleanup(applyDefaultDark)

		m := cursorTestModel(t, "hello\n", &config.Config{})
		m.mode = ModeCommand
		// A command with no completions: an empty cmdBuf lists every ex
		// command in a popup that covers the buffer line under test.
		m.cmdBuf = "zzzznosuchcommand"
		if out := m.View().Content; !strings.Contains(out, normalCursorStyle.Render("h")) {
			t.Error("command mode should keep painting the buffer cursor cell: the real cursor " +
				"has moved to the prompt, so nothing else shows where the buffer cursor is")
		}
	})
}

// TestNoCursorWhenOffScreen covers the guard against emitting a position
// outside the frame — a cursor scrolled out of view must be dropped, not
// clamped to an arbitrary edge cell.
func TestNoCursorWhenOffScreen(t *testing.T) {
	m := cursorTestModel(t, strings.Repeat("line\n", 200), &config.Config{})
	m.cursor.Line = 150 // far below the viewport, which is still scrolled to the top

	if c := m.View().Cursor; c != nil && c.Y >= m.visibleLines() {
		t.Errorf("cursor at row %d is outside the %d visible rows; it should have been dropped",
			c.Y, m.visibleLines())
	}
}
