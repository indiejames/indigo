package client

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestKeyReleaseDoesNotTriggerBindings is a regression test for a Bubble Tea
// v2 migration hazard: v1's tea.KeyMsg was a concrete struct, but v2's is an
// *interface* satisfied by both tea.KeyPressMsg and tea.KeyReleaseMsg. Every
// `case tea.KeyMsg` / `msg.(tea.KeyMsg)` dispatch in this package and in
// internal/app therefore matches a release as readily as a press, so without
// an explicit guard each keystroke would run its binding twice — once down,
// once up — the moment key releases start arriving.
//
// Releases are only emitted once the Kitty keyboard protocol is negotiated,
// which indigo doesn't currently request, so this is latent rather than
// live. That's precisely why it needs a test: the day someone enables
// tea.KeyboardEnhancements, the breakage would otherwise be silent and
// bizarre (every command firing twice) rather than a caught regression.
func TestKeyReleaseDoesNotTriggerBindings(t *testing.T) {
	// Ctrl+End goes to the end of the file — an unambiguous, cheap-to-observe
	// binding.
	press := tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl}
	release := tea.KeyReleaseMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl}

	t.Run("press runs the binding", func(t *testing.T) {
		m := newTestModel("hello\nworld\n")
		got, _ := m.Update(press)
		if got.(Model).cursor.Line == 0 {
			t.Fatalf("cursor after KeyPressMsg Ctrl+End = %+v, want it moved — "+
				"the test's premise (that Ctrl+End is a live binding) no longer holds",
				got.(Model).cursor)
		}
	})

	t.Run("release is ignored", func(t *testing.T) {
		m := newTestModel("hello\nworld\n")
		got, cmd := m.Update(release)
		if c := got.(Model).cursor; c.Line != 0 || c.Col != 0 {
			t.Errorf("cursor after KeyReleaseMsg Ctrl+End = %+v, want unmoved: "+
				"the release re-ran the binding", c)
		}
		if cmd != nil {
			t.Errorf("KeyReleaseMsg produced a non-nil command; releases should be dropped outright")
		}
	})

	// A release arriving mid-insert must not self-insert its character
	// either — the insert-mode path reads Key().Text, which a release
	// populates exactly like a press.
	t.Run("release does not self-insert", func(t *testing.T) {
		m := newTestModel("")
		m.mode = ModeInsert
		got, _ := m.Update(tea.KeyReleaseMsg{Code: 'x', Text: "x"})
		if content := got.(Model).buf.Content(); content != "" {
			t.Errorf("buffer after KeyReleaseMsg 'x' = %q, want empty: the release was self-inserted", content)
		}
	})
}
