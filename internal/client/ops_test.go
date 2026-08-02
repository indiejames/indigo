package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
)

func TestInsertEndPos(t *testing.T) {
	tests := []struct {
		fromLine, fromCol int
		text              string
		wantLine, wantCol int
	}{
		{0, 0, "hello", 0, 5},
		{0, 0, "", 0, 0},
		{0, 5, " world", 0, 11},
		{0, 0, "\n", 1, 0},
		{0, 0, "hello\nworld", 1, 5},
		{0, 0, "a\nb\nc", 2, 1},
		{2, 3, "abc\ndef\n", 4, 0},
		{1, 0, "\n\n\n", 4, 0},
	}
	for _, tt := range tests {
		gotLine, gotCol := insertEndPos(tt.fromLine, tt.fromCol, tt.text)
		if gotLine != tt.wantLine || gotCol != tt.wantCol {
			t.Errorf("insertEndPos(%d, %d, %q) = (%d, %d), want (%d, %d)",
				tt.fromLine, tt.fromCol, tt.text,
				gotLine, gotCol, tt.wantLine, tt.wantCol)
		}
	}
}

func TestBufText(t *testing.T) {
	m := Model{buf: document.New("", "hello\nworld\nfoo")}

	tests := []struct {
		fromLine, fromCol, toLine, toCol int
		want                             string
	}{
		{0, 0, 0, 5, "hello"},
		{0, 1, 0, 4, "ell"},
		{1, 0, 1, 5, "world"},
		{0, 3, 1, 2, "lo\nwo"},
		{0, 0, 2, 3, "hello\nworld\nfoo"},
		{0, 5, 1, 0, "\n"}, // just the newline
	}
	for _, tt := range tests {
		got := bufText(m, tt.fromLine, tt.fromCol, tt.toLine, tt.toCol)
		if got != tt.want {
			t.Errorf("bufText(%d,%d,%d,%d) = %q, want %q",
				tt.fromLine, tt.fromCol, tt.toLine, tt.toCol, got, tt.want)
		}
	}
}

func TestInverseOpInsert(t *testing.T) {
	m := Model{buf: document.New("", "hello world")}
	op := document.Op{
		Type:       document.OpInsert,
		InsertLine: 0,
		InsertCol:  5,
		InsertText: " there",
	}
	inv := inverseOp(m, op)
	if inv.Type != document.OpDelete {
		t.Fatalf("inverse of insert should be delete, got %v", inv.Type)
	}
	if inv.FromLine != 0 || inv.FromCol != 5 {
		t.Errorf("inv.From = (%d,%d), want (0,5)", inv.FromLine, inv.FromCol)
	}
	if inv.ToLine != 0 || inv.ToCol != 11 {
		t.Errorf("inv.To = (%d,%d), want (0,11)", inv.ToLine, inv.ToCol)
	}
}

func TestInverseOpDelete(t *testing.T) {
	m := Model{buf: document.New("", "hello world")}
	op := document.Op{
		Type:     document.OpDelete,
		FromLine: 0, FromCol: 5,
		ToLine: 0, ToCol: 11,
	}
	inv := inverseOp(m, op)
	if inv.Type != document.OpInsert {
		t.Fatalf("inverse of delete should be insert, got %v", inv.Type)
	}
	if inv.InsertLine != 0 || inv.InsertCol != 5 {
		t.Errorf("inv.Insert = (%d,%d), want (0,5)", inv.InsertLine, inv.InsertCol)
	}
	if inv.InsertText != " world" {
		t.Errorf("inv.InsertText = %q, want %q", inv.InsertText, " world")
	}
}

// TestApplyOpClearsStaleLSPOverlaysOnLineCountChange is a regression test for
// a reported bug: pressing Enter in insert mode caused colors below the
// cursor to visibly shift for about a second, then snap back. Root cause:
// m.semanticSpans/m.inlayHints are keyed by absolute line number but are only
// refreshed periodically (~1.2s), not on every edit. A line-count-changing
// edit shifts every subsequent line's number without re-indexing these
// caches, so they briefly rendered pre-edit data at now-wrong line numbers.
// applyOp must clear both immediately when the edit changes the line count.
func TestApplyOpClearsStaleLSPOverlaysOnLineCountChange(t *testing.T) {
	m := newTestModel("line one\nline two\n")
	m.rpc = &RPC{}
	m.semanticSpans = highlight.LineSpans{1: {{StartCol: 0, EndCol: 4, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 1, Col: 0, Label: "x"}}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "\n"}
	m2, _ := applyOp(m, op)

	if len(m2.semanticSpans) != 0 {
		t.Errorf("semanticSpans = %v, want empty after a line-count-changing edit", m2.semanticSpans)
	}
	if len(m2.inlayHints) != 0 {
		t.Errorf("inlayHints = %v, want empty after a line-count-changing edit", m2.inlayHints)
	}
}

// TestApplyOpClearsStaleLSPOverlaysOnSameLineEdit verifies a same-line,
// non-line-count-changing edit also clears that line's cache. A cached
// semantic span's columns are just as stale after a character is inserted
// before it as a whole line's number is after a newline — reported as
// visible "colors shift over" on a single line, not just after pressing
// Enter.
func TestApplyOpClearsStaleLSPOverlaysOnSameLineEdit(t *testing.T) {
	m := newTestModel("line one\n")
	m.rpc = &RPC{}
	m.semanticSpans = highlight.LineSpans{0: {{StartCol: 5, EndCol: 8, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 0, Col: 8, Label: "x"}}

	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}
	m2, _ := applyOp(m, op)

	if len(m2.semanticSpans[0]) != 0 {
		t.Errorf("semanticSpans[0] = %v, want cleared after an edit on line 0", m2.semanticSpans[0])
	}
	if len(m2.inlayHints) != 0 {
		t.Errorf("inlayHints = %v, want cleared after an edit on line 0", m2.inlayHints)
	}
}

// TestApplyOpKeepsLSPOverlaysBeforeEditLine is the counterpart to the two
// regression tests above: an edit can never invalidate a position strictly
// before it, so cached data for earlier lines must survive. This is the
// actual fix for the "whole file briefly goes uncolored on every keystroke"
// regression an earlier version of this fix introduced by clearing the
// entire cache unconditionally instead of just the affected lines.
func TestApplyOpKeepsLSPOverlaysBeforeEditLine(t *testing.T) {
	m := newTestModel("line one\nline two\nline three\n")
	m.rpc = &RPC{}
	m.semanticSpans = highlight.LineSpans{
		0: {{StartCol: 0, EndCol: 4, ANSI: "x"}},
		2: {{StartCol: 0, EndCol: 4, ANSI: "y"}},
	}
	m.inlayHints = []ClientInlayHint{{Line: 0, Col: 4, Label: "x"}}

	// Edit on line 2 — line 0's cached data is unrelated and must survive.
	op := document.Op{Type: document.OpInsert, InsertLine: 2, InsertCol: 0, InsertText: "X"}
	m2, _ := applyOp(m, op)

	if len(m2.semanticSpans[0]) != 1 {
		t.Errorf("semanticSpans[0] = %v, want unchanged (edit was on line 2)", m2.semanticSpans[0])
	}
	if len(m2.semanticSpans[2]) != 0 {
		t.Errorf("semanticSpans[2] = %v, want cleared (the edited line)", m2.semanticSpans[2])
	}
	if len(m2.inlayHints) != 1 {
		t.Errorf("inlayHints = %v, want unchanged (edit was on line 2)", m2.inlayHints)
	}
}

// TestInvalidateLSPOverlaysDebounceCoalescesRapidEdits verifies the
// lspOverlaySeq token: a burst of edits (the common case while typing)
// invalidates repeatedly, but only the LATEST scheduled refresh should
// actually fire — earlier ones must be recognized as stale by the
// lspOverlayRefreshMsg handler and ignored, so rapid typing coalesces into
// one LSP round trip instead of one per keystroke.
func TestInvalidateLSPOverlaysDebounceCoalescesRapidEdits(t *testing.T) {
	m := newTestModel("line one\n")
	m.rpc = &RPC{}
	m.cfg = &config.Config{SemanticTokens: true, InlayHints: true}

	m, _ = m.invalidateLSPOverlaysFrom(0)
	firstSeq := m.lspOverlaySeq
	m, _ = m.invalidateLSPOverlaysFrom(0)
	secondSeq := m.lspOverlaySeq

	if firstSeq == secondSeq {
		t.Fatal("setup: expected two successive invalidations to produce different seq tokens")
	}

	// The stale (first) refresh message must be ignored.
	res, cmd := m.Update(lspOverlayRefreshMsg{seq: firstSeq})
	m2 := res.(Model)
	if cmd != nil {
		t.Error("a stale lspOverlayRefreshMsg should not trigger a re-fetch")
	}
	_ = m2

	// The current (second/latest) refresh message must be accepted (non-nil
	// Cmd — the actual fetch is async and RPC-backed, not asserted here).
	_, cmd2 := m.Update(lspOverlayRefreshMsg{seq: secondSeq})
	if cmd2 == nil {
		t.Error("the current lspOverlayRefreshMsg should trigger a re-fetch")
	}
}
