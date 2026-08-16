package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/highlight"
)

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
