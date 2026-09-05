package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func cursorTestApp(t *testing.T, nBuffers int, cfg *config.Config) App {
	t.Helper()
	bufs := make([]client.Model, 0, nBuffers)
	for i := range nBuffers {
		m := client.New(&client.RPC{}, uint32(i+1), "hello\nworld\n", 0, "/tmp/a.go", "/tmp", cfg, false, 0)
		sized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		bufs = append(bufs, sized.(client.Model))
	}
	// -1 is "no file-changed prompt"; the zero value would read as "prompt
	// showing for buffer 0" and suppress the cursor.
	return App{buffers: bufs, active: 0, width: 80, height: 24, cfg: cfg, fileChangedIdx: -1}
}

// TestAppCursorShiftsForTabBar checks the buffer's cursor row is offset by
// the tab bar App draws above it. Without the shift the terminal cursor sits
// one row above where typing actually lands, on the tab bar itself.
func TestAppCursorShiftsForTabBar(t *testing.T) {
	cfg := &config.Config{}

	single := cursorTestApp(t, 1, cfg)
	if single.showTabBar() {
		t.Fatal("premise: one buffer should not show a tab bar")
	}
	noBar := single.View().Cursor
	if noBar == nil {
		t.Fatal("no cursor with a single buffer")
	}

	multi := cursorTestApp(t, 2, cfg)
	if !multi.showTabBar() {
		t.Fatal("premise: two buffers should show a tab bar")
	}
	withBar := multi.View().Cursor
	if withBar == nil {
		t.Fatal("no cursor with a tab bar showing")
	}

	if withBar.Y != noBar.Y+1 {
		t.Errorf("cursor row with tab bar = %d, without = %d; want exactly one row lower",
			withBar.Y, noBar.Y)
	}
	if withBar.X != noBar.X {
		t.Errorf("tab bar changed the cursor column (%d vs %d); it should only shift rows",
			withBar.X, noBar.X)
	}
}

// TestAppHidesCursorUnderDialogs verifies the buffer's terminal cursor is
// dropped whenever something is layered over the buffer. Each of these
// dialogs draws its own cursor in its own text input, so leaving the
// buffer's real cursor visible would strand a second one behind the dialog.
func TestAppHidesCursorUnderDialogs(t *testing.T) {
	cfg := &config.Config{}

	base := cursorTestApp(t, 1, cfg)
	if base.View().Cursor == nil {
		t.Fatal("premise: a plain buffer view should have a cursor")
	}

	cases := []struct {
		name string
		mut  func(a *App)
	}{
		{"search & replace", func(a *App) { a.searchReplace = newSearchReplaceDialog("/tmp", 80, 24) }},
		{"buffer picker", func(a *App) { a.bufPicker = &bufPicker{} }},
		{"file-changed prompt", func(a *App) { a.fileChangedIdx = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := cursorTestApp(t, 1, cfg)
			a.fileChangedIdx = -1
			tc.mut(&a)
			if c := a.View().Cursor; c != nil {
				t.Errorf("cursor still shown at (%d,%d) with the %s open; it belongs to the "+
					"buffer underneath and is now hidden behind the dialog", c.X, c.Y, tc.name)
			}
		})
	}
}

// TestAppHidesCursorWithNoBuffer covers the empty state: there's no buffer,
// so there's nothing for a cursor to point at.
func TestAppHidesCursorWithNoBuffer(t *testing.T) {
	a := App{buffers: nil, width: 80, height: 24, cfg: &config.Config{}, fileChangedIdx: -1}
	if c := a.View().Cursor; c != nil {
		t.Errorf("cursor shown at (%d,%d) with no buffer open", c.X, c.Y)
	}
}
