package client

import (
	"errors"
	"testing"

	"github.com/indiejames/indigo/internal/highlight"
)

var errBoom = errors.New("boom")

// TestFormatResultDiscardsStaleBufID is a regression test: formatResultMsg
// used to carry no bufID at all, so a slow format result that arrived after
// the user switched to a different buffer would unconditionally replace
// that now-active buffer's content — silent cross-buffer corruption, worse
// than the "stale render" class of bug the other bufID guards in this file
// protect against, since this one actually overwrites file content.
func TestFormatResultDiscardsStaleBufID(t *testing.T) {
	m := newTestModel("original content\n")
	m.bufID = 1

	m2, cmd := m.Update(formatResultMsg{bufID: 2, changed: true})
	got := m2.(Model)

	if got.buf.Content() != "original content\n" {
		t.Errorf("buf.Content() = %q, want the original content untouched (stale result from bufID 2, model is bufID 1)", got.buf.Content())
	}
	if cmd != nil {
		t.Error("expected a nil cmd for a discarded stale-bufID result")
	}
}

// TestFormatResultErrDiscardsStaleBufID is a regression test: fetchFormat
// used to report an RPC failure via the unscoped errorMsg, so a formatting
// error from one buffer could show up in the status bar of whatever buffer
// the user had since switched to. formatResultMsg now carries the error
// itself, so the same bufID staleness guard that already protects the
// success path also protects the error path.
func TestFormatResultErrDiscardsStaleBufID(t *testing.T) {
	m := newTestModel("original content\n")
	m.bufID = 1

	m2, cmd := m.Update(formatResultMsg{bufID: 2, err: errBoom})
	got := m2.(Model)

	if got.status != "" {
		t.Errorf("status = %q, want empty — a stale-bufID format error must not be shown", got.status)
	}
	if cmd != nil {
		t.Error("expected a nil cmd for a discarded stale-bufID error result")
	}
}

// TestFormatResultErrShowsStatusForCurrentBuffer verifies the non-stale case:
// a format error for the currently active buffer is surfaced in the status bar.
func TestFormatResultErrShowsStatusForCurrentBuffer(t *testing.T) {
	m := newTestModel("original content\n")
	m.bufID = 1

	m2, _ := m.Update(formatResultMsg{bufID: 1, err: errBoom})
	got := m2.(Model)

	if got.status == "" {
		t.Error("status should be set after a format error for the currently active buffer")
	}
}

// TestFormatResultNoFormatterStatusSurvivesTheFollowingSave is a regression
// test for a live bug report: format-on-save with no formatter available
// (or nothing to format) silently saved with zero feedback — worse than a
// missing message, since the status WAS pushed by formatResultMsg but the
// save that runs immediately after (thenSave) resolves via savedMsg, whose
// success handler unconditionally does pushStatus(""), clearing it before
// the user could ever see it. keepStatusOnNextSave must protect it for
// exactly one save.
func TestFormatResultNoFormatterStatusSurvivesTheFollowingSave(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.bufID = 1

	m2, _ := m.Update(formatResultMsg{bufID: 1, changed: false, noFormatter: true, thenSave: true})
	got := m2.(Model)
	if got.status != "No formatter available" {
		t.Fatalf("status = %q, want %q", got.status, "No formatter available")
	}
	if !got.keepStatusOnNextSave {
		t.Fatal("keepStatusOnNextSave should be set after a format-on-save found no formatter")
	}

	m3, _ := got.Update(savedMsg{bufID: 1, version: got.buf.Version()})
	got3 := m3.(Model)
	if got3.status != "No formatter available" {
		t.Errorf("status after the following save = %q, want it to survive as %q", got3.status, "No formatter available")
	}
	if got3.keepStatusOnNextSave {
		t.Error("keepStatusOnNextSave should be consumed (reset to false) after protecting one save")
	}
}

// TestFormatResultKeepStatusFlagDoesNotLeakPastAFailedSave guards the
// robustness fix alongside the above: if the save following a no-formatter
// format-on-save fails instead of succeeding, keepStatusOnNextSave must
// still be reset — otherwise it would leak and incorrectly protect a later,
// unrelated save's status from being cleared.
func TestFormatResultKeepStatusFlagDoesNotLeakPastAFailedSave(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.bufID = 1

	m2, _ := m.Update(formatResultMsg{bufID: 1, changed: false, noFormatter: true, thenSave: true})
	got := m2.(Model)

	m3, _ := got.Update(saveFailedMsg{bufID: 1, err: errBoom})
	got3 := m3.(Model)
	if got3.keepStatusOnNextSave {
		t.Error("keepStatusOnNextSave should be reset after a failed save, not left set for a later unrelated save")
	}

	m4, _ := got3.Update(savedMsg{bufID: 1, version: got3.buf.Version()})
	got4 := m4.(Model)
	if got4.status != "" {
		t.Errorf("status after a later, unrelated successful save = %q, want cleared", got4.status)
	}
}

// TestFormatResultKeepStatusFlagDoesNotLeakPastAVersionMismatch guards
// another leak path a review caught alongside the failed-save one above:
// if the save following a no-formatter format-on-save is superseded by a
// concurrent edit (savedMsg/savedAsMsg's own version-mismatch guard, which
// returns early without ever reaching the flag-consuming code), the flag
// must still be reset — otherwise it leaks into a later, unrelated save.
func TestFormatResultKeepStatusFlagDoesNotLeakPastAVersionMismatch(t *testing.T) {
	t.Run("savedMsg", func(t *testing.T) {
		m := newTestModel("func foo() {}\n")
		m.bufID = 1

		m2, _ := m.Update(formatResultMsg{bufID: 1, changed: false, noFormatter: true, thenSave: true})
		got := m2.(Model)

		// A version that doesn't match the buffer's current version
		// simulates a concurrent edit landing before the save's response
		// arrived — savedMsg's staleness guard returns early.
		m3, _ := got.Update(savedMsg{bufID: 1, version: got.buf.Version() + 1})
		got3 := m3.(Model)
		if got3.keepStatusOnNextSave {
			t.Error("keepStatusOnNextSave should be reset after a superseded (version-mismatch) savedMsg")
		}

		m4, _ := got3.Update(savedMsg{bufID: 1, version: got3.buf.Version()})
		got4 := m4.(Model)
		if got4.status != "" {
			t.Errorf("status after a later, unrelated successful save = %q, want cleared", got4.status)
		}
	})

	t.Run("savedAsMsg", func(t *testing.T) {
		m := newTestModel("func foo() {}\n")
		m.bufID = 1

		m2, _ := m.Update(formatResultMsg{bufID: 1, changed: false, noFormatter: true, thenSave: true})
		got := m2.(Model)

		m3, _ := got.Update(savedAsMsg{bufID: 1, version: got.buf.Version() + 1, newPath: "/tmp/x.go"})
		got3 := m3.(Model)
		if got3.keepStatusOnNextSave {
			t.Error("keepStatusOnNextSave should be reset after a superseded (version-mismatch) savedAsMsg")
		}

		m4, _ := got3.Update(savedMsg{bufID: 1, version: got3.buf.Version()})
		got4 := m4.(Model)
		if got4.status != "" {
			t.Errorf("status after a later, unrelated successful save = %q, want cleared", got4.status)
		}
	})
}

// TestFormatResultUnchangedDoesNotTouchHighlighting verifies the no-op case
// (content already formatted) leaves caches alone — there's nothing to
// refresh since the buffer didn't change.
func TestFormatResultUnchangedDoesNotTouchHighlighting(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.semanticSpans = highlight.LineSpans{0: {{StartCol: 0, EndCol: 4}}}

	m2, _ := m.Update(formatResultMsg{changed: false})
	got := m2.(Model)

	if got.semanticSpans == nil {
		t.Error("semanticSpans should be left alone when the formatter made no change")
	}
}
