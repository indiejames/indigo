package client

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const longWarning = "W: attached to process 30544, but this program runs its TypeScript through tsx, which gives a debugger no source maps, so breakpoints in its .ts files cannot bind; run it with `node <file>.ts` and attach, or launch it from indigo"

func press(m Model, code rune, text string) Model {
	updated, _ := m.Update(tea.KeyPressMsg{Code: code, Text: text})
	return updated.(Model)
}

// A warning survives ordinary keys — the next thing a user does after
// attaching is move to a line and set a breakpoint, which used to wipe it
// before it could be read — and Esc dismisses it. Errors keep clearing on any
// key, as before.
func TestWarningSurvivesKeys(t *testing.T) {
	m := newTestModel("a\nb\nc\n").pushStatus(longWarning)
	m = press(m, 'j', "j")
	m = press(m, 'k', "k")
	if m.status != longWarning {
		t.Errorf("after moving: status = %q, want the warning kept", m.status)
	}
	m = press(m, tea.KeyEscape, "")
	if m.status != "" {
		t.Errorf("after Esc: status = %q, want it dismissed", m.status)
	}
	m = m.pushStatus("E: something failed")
	if m = press(m, 'j', "j"); m.status != "" {
		t.Errorf("an error should still clear on a key: %q", m.status)
	}
}

// A warning lasts long enough to read — longer for more text — and then goes.
func TestWarningLifetime(t *testing.T) {
	if got := toastLifetime(longWarning); got < 10*time.Second {
		t.Errorf("a %d-character warning lasts %v", len(longWarning), got)
	}
	if got := toastLifetime("W: short"); got != toastDuration {
		t.Errorf("a short warning lasts %v, want the toast minimum", got)
	}
	if got := toastLifetime("E: " + strings.Repeat("x", 400)); got != toastDuration {
		t.Errorf("errors are unchanged: %v", got)
	}
	m := newTestModel("a\n").pushStatus(longWarning)
	m.statusAt = time.Now().Add(-toastDuration - time.Second) // past an error's life, not this one's
	updated, _ := m.Update(tickMsg{})
	if updated.(Model).status != longWarning {
		t.Error("the warning expired on an error's schedule")
	}
	m.statusAt = time.Now().Add(-toastLifetime(longWarning) - time.Second)
	updated, _ = m.Update(tickMsg{})
	if updated.(Model).status != "" {
		t.Error("the warning never expired")
	}
	// Past its lifetime, a key clears it like any other status.
	m = m.pushStatus(longWarning)
	m.statusAt = time.Now().Add(-time.Hour)
	if m = press(m, 'j', "j"); m.status != "" {
		t.Error("an expired warning survived a key")
	}
}

// The message log shows a long entry in full, wrapped, rather than cut off.
func TestMessageLogWrapsLongEntries(t *testing.T) {
	log := []logEntry{{at: time.Date(2026, 9, 27, 19, 9, 0, 0, time.Local), text: longWarning}}
	lines := messageLogPopupLines(log, 60)
	if len(lines) < 3 {
		t.Fatalf("got %d lines, want the entry wrapped", len(lines))
	}
	var joined []string
	for i, l := range lines {
		plain := ansi.Strip(l)
		if w := ansi.StringWidth(plain); w > 60 {
			t.Errorf("line %d is %d wide", i, w)
		}
		if i > 0 && !strings.HasPrefix(plain, "          ") {
			t.Errorf("continuation line %d not indented under the text: %q", i, plain)
		}
		joined = append(joined, strings.TrimSpace(strings.TrimPrefix(plain, "19:09:00")))
	}
	if got := strings.Join(joined, " "); got != longWarning {
		t.Errorf("text lost in wrapping:\n got %q\nwant %q", got, longWarning)
	}
	// Scrolling counts wrapped lines, so it can reach the end of a long entry.
	if lines := messageLogPopupLines(log, messageLogInnerWidth(70)); messageLogMaxScroll(70, 6, log) != max(0, len(lines)-(max(6, 6-5)-2)) {
		t.Error("scroll range does not match the wrapped lines")
	}
}
