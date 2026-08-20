package client

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestIsErrMessage(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"E: pattern not found", true},
		{"ERR: something broke", true},
		{"Renamed 3 occurrence(s) across 2 file(s)", false},
		{"", false},
		{"Errantly similar but not prefixed", false},
	}
	for _, c := range cases {
		if got := isErrMessage(c.text); got != c.want {
			t.Errorf("isErrMessage(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestPushStatusErrorClassIsExcludedFromStatusBar verifies error-class text
// no longer shows in the status bar's center segment (it renders as the
// toast overlay instead — see renderStatusBar's isErrMessage guard).
func TestPushStatusErrorClassIsExcludedFromStatusBar(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushStatus("E: pattern not found")

	bar := m.renderStatusBar()
	if strings.Contains(bar, "pattern not found") {
		t.Errorf("status bar should not contain error-class text, got %q", bar)
	}
}

// TestPushStatusNonErrorStaysInStatusBar is the control case for the above:
// ordinary informational status text is unaffected by the toast change.
func TestPushStatusNonErrorStaysInStatusBar(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushStatus("Organized imports")

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "Organized imports") {
		t.Errorf("status bar should still show non-error status text, got %q", bar)
	}
}

// TestTickExpiresErrorToastAfterDuration is a regression test for the toast
// overlay's auto-dismiss: an error-class status message should be cleared by
// the tickMsg handler once toastDuration has elapsed since it was pushed.
func TestTickExpiresErrorToastAfterDuration(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushStatus("E: pattern not found")
	m.statusAt = time.Now().Add(-toastDuration - time.Second)

	updated, _ := m.Update(tickMsg{})
	m2 := updated.(Model)

	if m2.status != "" {
		t.Errorf("status = %q, want cleared after toastDuration elapsed", m2.status)
	}
}

// TestTickDoesNotExpireNonErrorStatus verifies the expiry only applies to
// error-class text — an ordinary status message persists until replaced,
// matching pre-toast behavior.
func TestTickDoesNotExpireNonErrorStatus(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushStatus("Organized imports")
	m.statusAt = time.Now().Add(-toastDuration - time.Second)

	updated, _ := m.Update(tickMsg{})
	m2 := updated.(Model)

	if m2.status != "Organized imports" {
		t.Errorf("status = %q, want unchanged (non-error text never expires)", m2.status)
	}
}

// TestTickDoesNotExpireFreshErrorToast guards against an off-by-one: a toast
// pushed just now must survive the very next tick.
func TestTickDoesNotExpireFreshErrorToast(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushStatus("E: pattern not found")

	updated, _ := m.Update(tickMsg{})
	m2 := updated.(Model)

	if m2.status == "" {
		t.Error("a freshly pushed error toast should not expire on the immediately following tick")
	}
}

// TestPushSevereErrorSetsModalNotStatus verifies pushSevereError shows the
// must-dismiss modal (severeErr) instead of the toast (status), while still
// recording the message in messageLog for later review.
func TestPushSevereErrorSetsModalNotStatus(t *testing.T) {
	m := newTestModel("hello\n")
	m = m.pushSevereError("ERR: resync failed, buffer may be out of sync with the server: boom")

	if m.severeErr == "" {
		t.Error("expected severeErr to be set")
	}
	if m.status != "" {
		t.Errorf("status = %q, want empty — severe errors must not also show as a toast", m.status)
	}
	if len(m.messageLog) != 1 || !m.messageLog[0].isErr {
		t.Errorf("messageLog = %+v, want one isErr entry", m.messageLog)
	}
}

// TestHandleKeySevereErrBlocksUntilDismissed verifies the severe-error modal
// swallows ordinary keys (so the user can't type through an unread,
// state-affecting failure notice) and is only dismissed by Enter or Esc.
func TestHandleKeySevereErrBlocksUntilDismissed(t *testing.T) {
	m := newTestModel("hello\n")
	m.severeErr = "buffer may be out of sync with the server"

	newModel, _ := m.handleKey(fakeKey("x"))
	m2 := newModel.(Model)
	if m2.severeErr == "" {
		t.Error("an ordinary key should not dismiss the severe-error modal")
	}
	if m2.buf.Content() != "hello\n" {
		t.Errorf("buf.Content() = %q, want unchanged — the key must be swallowed, not applied", m2.buf.Content())
	}

	newModel, _ = m2.handleKey(fakeKey("esc"))
	m3 := newModel.(Model)
	if m3.severeErr != "" {
		t.Errorf("severeErr = %q, want cleared after Esc", m3.severeErr)
	}
}

// TestHandleKeySevereErrDismissedByEnter mirrors the Esc case for Enter.
func TestHandleKeySevereErrDismissedByEnter(t *testing.T) {
	m := newTestModel("hello\n")
	m.severeErr = "buffer may be out of sync with the server"

	newModel, _ := m.handleKey(fakeKey("enter"))
	m2 := newModel.(Model)
	if m2.severeErr != "" {
		t.Errorf("severeErr = %q, want cleared after Enter", m2.severeErr)
	}
}

// --- render funcs ---

func TestRenderToastWrapsAndBorders(t *testing.T) {
	lines := renderToast("ERR: something long enough that it might have been truncated in the status bar before", 60)
	if len(lines) < 3 { // top + at least one body line + bottom
		t.Errorf("renderToast: got %d lines, want >= 3", len(lines))
	}
}

func TestRenderSevereErrorPopupIncludesDismissHint(t *testing.T) {
	lines := renderSevereErrorPopup("buffer may be out of sync with the server", 80, 20)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "dismiss") {
		t.Errorf("renderSevereErrorPopup should include a dismiss hint, got:\n%s", joined)
	}
}

// TestRenderSevereErrorPopupCapsHeightAndKeepsFooter is a regression test:
// renderSevereErrorPopup previously had no height cap, so a long message
// could push the bottom border and the dismiss-hint footer — the only
// visible instructions for closing a modal that blocks all input — past
// what the caller actually composites into the view.
func TestRenderSevereErrorPopupCapsHeightAndKeepsFooter(t *testing.T) {
	longText := strings.Repeat("this is a very long error message. ", 40)
	const maxH = 10
	lines := renderSevereErrorPopup(longText, 80, maxH)

	if len(lines) > maxH {
		t.Errorf("renderSevereErrorPopup: got %d lines, want <= maxH (%d)", len(lines), maxH)
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "dismiss") {
		t.Errorf("last line should still be the dismiss-hint footer even when truncated, got %q", last)
	}
}

// TestRenderToastInnerWidthNeverExceedsMaxW is a regression test: a forced
// minimum inner width used to let the toast box overflow maxW on a narrow
// terminal (render_view.go centers popups by column offset alone, with no
// clipping, so an oversized box corrupts the rendered line).
func TestRenderToastInnerWidthNeverExceedsMaxW(t *testing.T) {
	const maxW = 8
	lines := renderToast("E: short", maxW)
	for _, l := range lines {
		if w := lipgloss.Width(l); w > maxW {
			t.Errorf("renderToast line width = %d, want <= maxW (%d): %q", w, maxW, l)
		}
	}
}

// TestRenderSevereErrorPopupWidthNeverExceedsMaxW mirrors the toast case for
// the severe-error modal.
func TestRenderSevereErrorPopupWidthNeverExceedsMaxW(t *testing.T) {
	const maxW = 8
	lines := renderSevereErrorPopup("E: short", maxW, 10)
	for _, l := range lines {
		if w := lipgloss.Width(l); w > maxW {
			t.Errorf("renderSevereErrorPopup line width = %d, want <= maxW (%d): %q", w, maxW, l)
		}
	}
}

// TestMouseMsgBlockedBySevereError verifies mouse events are swallowed while
// the must-dismiss modal is up — handleKey's severeErr gate only guards
// tea.KeyMsg, but tea.MouseMsg is handled separately in Model.Update and
// bypasses it entirely unless also guarded there.
func TestMouseMsgBlockedBySevereError(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.severeErr = "buffer may be out of sync with the server"
	prevCursor := m.cursor
	prevTopLine := m.topLine

	updated, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 0, Y: 1})
	m2 := updated.(Model)

	if cmd != nil {
		t.Error("expected no command from a mouse event while the severe-error modal is up")
	}
	if m2.cursor != prevCursor || m2.topLine != prevTopLine {
		t.Error("mouse event should not move the cursor or scroll while the severe-error modal is up")
	}
}
