package client

import (
	"errors"
	"testing"

	"github.com/indiejames/indigo/internal/highlight"
)

var errBoom = errors.New("boom")

// TestFormatResultRefreshesHighlighting is a regression test: formatResultMsg
// used to swap in the reformatted buffer without ever reparsing tree-sitter
// highlighting or invalidating the semantic-token/inlay-hint caches, so
// moved code kept whatever coloring applied to its old position until an
// unrelated edit happened to trigger a reparse.
func TestFormatResultRefreshesHighlighting(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.semanticSpans = highlight.LineSpans{0: {{StartCol: 0, EndCol: 4}}}
	m.inlayHints = []ClientInlayHint{{Line: 0, Col: 5, Label: "int"}}

	m2, cmd := m.Update(formatResultMsg{content: "func bar() {}\n", changed: true})
	got := m2.(Model)

	if got.buf.Content() != "func bar() {}\n" {
		t.Errorf("buf.Content() = %q, want %q", got.buf.Content(), "func bar() {}\n")
	}
	if got.semanticSpans != nil {
		t.Error("semanticSpans should be cleared after a reformat, not left at their pre-format positions")
	}
	if got.inlayHints != nil {
		t.Error("inlayHints should be cleared after a reformat, not left at their pre-format positions")
	}
	if cmd == nil {
		t.Error("expected a non-nil cmd to reparse highlighting and refresh LSP overlays")
	}
}

// TestFormatResultAdoptsGenerationOnChange is a regression test: Format is
// one of the RPCs that wholesale-swaps the buffer object on the server when
// it makes a change (format-on-save, explicit format), the same as
// openFile/getUpdates/getBufferSnapshot — but formatResultMsg used to carry
// no generation at all, so the very next updatesMsg poll would see the
// server's bumped generation, not recognize it as this client's own
// change, and spuriously trigger resyncFromServer() (with its user-facing
// "Buffer resynced from server" severe-error modal) even though nothing
// was actually wrong. The handler must adopt msg.generation into
// m.generation on the changed path so that poll no longer looks like a
// foreign swap.
func TestFormatResultAdoptsGenerationOnChange(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.generation = 3
	m.generationKnown = true

	m2, _ := m.Update(formatResultMsg{content: "func bar() {}\n", changed: true, generation: 4})
	got := m2.(Model)

	if got.generation != 4 {
		t.Errorf("generation = %d, want 4 (adopted from the format result)", got.generation)
	}
	if !got.generationKnown {
		t.Error("generationKnown should remain true after adopting a format result's generation")
	}

	m3, cmd := got.Update(updatesMsg{generation: 4})
	got3 := m3.(Model)
	if got3.severeErr != "" {
		t.Errorf("severeErr = %q, want empty — a poll matching the just-adopted generation must not trigger a resync", got3.severeErr)
	}
	_ = cmd
}

// TestFormatResultUnchangedLeavesGenerationAlone is a regression test
// alongside the one above: when Format reports changed=false, the server
// never swapped the buffer, so generation is meaningless (left at its zero
// value) and must not overwrite the client's real remembered generation.
func TestFormatResultUnchangedLeavesGenerationAlone(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.generation = 3
	m.generationKnown = true

	m2, _ := m.Update(formatResultMsg{changed: false, noFormatter: true, generation: 0})
	got := m2.(Model)

	if got.generation != 3 {
		t.Errorf("generation = %d, want unchanged at 3 (changed=false means no server-side swap happened)", got.generation)
	}
}

// TestFormatResultResetsVersionOnChange is a regression test:
// discardRecoveryMsg's handler (the sibling wholesale-buffer-swap case)
// already resets m.version to 0 right after replacing m.buf with a fresh
// document.New, since a fresh buffer always starts at version 0 on the
// server too. formatResultMsg's changed branch does the identical
// document.New swap but was missing the matching m.version reset — left
// at a stale pre-format value, a later GetUpdates poll's sinceVersion
// could race ahead of the server's actual (reset) version, silently
// skipping a real op from another client that lands in that window.
func TestFormatResultResetsVersionOnChange(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.version = 7

	m2, _ := m.Update(formatResultMsg{content: "func bar() {}\n", changed: true, generation: 1})
	got := m2.(Model)

	if got.version != 0 {
		t.Errorf("version = %d, want 0 (reset to match the fresh post-format buffer)", got.version)
	}
}

// TestFormatResultDiscardsStaleBufID is a regression test: formatResultMsg
// used to carry no bufID at all, so a slow format result that arrived after
// the user switched to a different buffer would unconditionally replace
// that now-active buffer's content — silent cross-buffer corruption, worse
// than the "stale render" class of bug the other bufID guards in this file
// protect against, since this one actually overwrites file content.
func TestFormatResultDiscardsStaleBufID(t *testing.T) {
	m := newTestModel("original content\n")
	m.bufID = 1

	m2, cmd := m.Update(formatResultMsg{bufID: 2, content: "formatted content from a different buffer\n", changed: true})
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

// TestFormatResultUnchangedDoesNotTouchHighlighting verifies the no-op case
// (content already formatted) leaves caches alone — there's nothing to
// refresh since the buffer didn't change.
func TestFormatResultUnchangedDoesNotTouchHighlighting(t *testing.T) {
	m := newTestModel("func foo() {}\n")
	m.semanticSpans = highlight.LineSpans{0: {{StartCol: 0, EndCol: 4}}}

	m2, _ := m.Update(formatResultMsg{content: "func foo() {}\n", changed: false})
	got := m2.(Model)

	if got.semanticSpans == nil {
		t.Error("semanticSpans should be left alone when the formatter made no change")
	}
}
