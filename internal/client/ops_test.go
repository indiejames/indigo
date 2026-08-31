package client

import (
	"reflect"
	"strings"
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

// TestApplyOpPreservesLSPOverlaysAcrossEdits verifies applyOp does NOT clear
// m.semanticSpans/m.inlayHints, for either a line-count-changing edit
// (Enter) or a same-line edit. An earlier version of this fix cleared them
// immediately for positional correctness, but since tree-sitter doesn't
// capture plain identifiers at all in most grammars, that meant those
// identifiers flashed to the terminal's default (often white) color on every
// keystroke — visibly worse than the brief mispositioning it prevented.
// Mirroring VS Code's own approach, stale data is now left on screen until a
// debounced re-fetch (see scheduleLSPOverlayRefresh) replaces it; the render
// path's bounds check (see TestRenderLineChunkSkipsStaleSpanPastLineEnd) is
// what keeps a stale span from ever painting somewhere it shouldn't.
func TestApplyOpPreservesLSPOverlaysAcrossEdits(t *testing.T) {
	m := newTestModel("line one\nline two\n")
	m.rpc = &RPC{}
	m.semanticSpans = highlight.LineSpans{1: {{StartCol: 0, EndCol: 4, ANSI: "x"}}}
	m.inlayHints = []ClientInlayHint{{Line: 1, Col: 0, Label: "x"}}

	// A line-count-changing edit (Enter) inserted at line 0 — data at line 1
	// shifts to line 2, it isn't cleared or left at the now-wrong old key.
	op := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "\n"}
	m2, _ := applyOp(m, op)
	if len(m2.semanticSpans[2]) != 1 || len(m2.inlayHints) != 1 || m2.inlayHints[0].Line != 2 {
		t.Errorf("line-count-changing edit lost or mis-shifted cached data: semanticSpans=%v inlayHints=%v",
			m2.semanticSpans, m2.inlayHints)
	}

	// A same-line, non-line-count-changing edit.
	m3 := newTestModel("line one\n")
	m3.rpc = &RPC{}
	m3.semanticSpans = highlight.LineSpans{0: {{StartCol: 5, EndCol: 8, ANSI: "x"}}}
	op2 := document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"}
	m4, _ := applyOp(m3, op2)
	if len(m4.semanticSpans[0]) != 1 {
		t.Errorf("same-line edit cleared cached data: semanticSpans=%v", m4.semanticSpans)
	}
}

// TestApplyOpScrollsViewportToKeepCursorVisible is a regression test: every
// applyOp call site (self-insert, backspace, tab, Enter, auto-pair, ...)
// already moves m.cursor to reflect the edit before calling applyOp, but
// applyOp itself never re-synced the viewport — so an edit that pushed the
// cursor past the bottom of the viewport (e.g. typing enough characters to
// soft-wrap the current line onto a new visual row while already at the
// last visible row) left it rendered off-screen for the rest of the insert
// session, since nothing else re-checks until the next cursor-moving key.
func TestApplyOpScrollsViewportToKeepCursorVisible(t *testing.T) {
	// cw = 5 (width 5, no gutter): "12345" is exactly one visual chunk;
	// appending a 6th character wraps it onto a second chunk.
	m := newTestModel("a\nb\n12345")
	m.rpc = &RPC{}
	m.cfg = &config.Config{ScrollOff: 0} // isolate from the scrolloff feature
	m.height = 4                         // visibleLines = 3
	m.width = 5
	m.topLine = 0
	m.cursor = document.Pos{Line: 2, Col: 5} // already at the last visible row

	op := document.Op{Type: document.OpInsert, InsertLine: 2, InsertCol: 5, InsertText: "6"}
	m.cursor.Col = 6 // caller-side cursor update, as every real call site does
	m2, _ := applyOp(m, op)

	if row, vis := m2.cursorVisualRowFromTop(m2.contentWidth()), m2.visibleLines(); row >= vis {
		t.Errorf("cursor scrolled off-screen after applyOp: row=%d, visibleLines=%d, topLine=%d", row, vis, m2.topLine)
	}
}

// TestApplyBatchScrollsViewportToKeepCursorVisible is applyBatch's
// counterpart to TestApplyOpScrollsViewportToKeepCursorVisible.
func TestApplyBatchScrollsViewportToKeepCursorVisible(t *testing.T) {
	m := newTestModel("a\nb\n12345")
	m.rpc = &RPC{}
	m.cfg = &config.Config{ScrollOff: 0}
	m.height = 4
	m.width = 5
	m.topLine = 0
	m.cursor = document.Pos{Line: 2, Col: 5}

	ops := []document.Op{{Type: document.OpInsert, InsertLine: 2, InsertCol: 5, InsertText: "6"}}
	m.cursor.Col = 6
	m2, _ := applyBatch(m, ops)

	if row, vis := m2.cursorVisualRowFromTop(m2.contentWidth()), m2.visibleLines(); row >= vis {
		t.Errorf("cursor scrolled off-screen after applyBatch: row=%d, visibleLines=%d, topLine=%d", row, vis, m2.topLine)
	}
}

// TestRenderLineChunkSkipsStaleSpanPastLineEnd is the actual safety net for
// no longer clearing on edit: a span whose StartCol no longer fits the
// (now-shorter) line — e.g. a semantic-token span computed before the user
// deleted most of the line — must be skipped, not rendered starting from
// column 0 through the end of the line. The naive fallback (defaulting to
// column 0) would paint the ENTIRE remaining line with the stale color; this
// was previously unreachable because tree-sitter spans are always freshly
// recomputed on every edit, but became reachable once semantic-token spans
// were allowed to outlive the edit that shifted them.
func TestRenderLineChunkSkipsStaleSpanPastLineEnd(t *testing.T) {
	m := newTestModel("ab\n")
	const staleColor = "\x1b[38;2;9;9;9m"
	// A stale span from before the line was shortened to "ab" — its start
	// column (10) no longer exists on the current 2-rune line.
	m.hlSpans = highlight.LineSpans{0: {{StartCol: 10, EndCol: 15, ANSI: staleColor}}}

	cw := 80
	layout := m.buildScreenLayout(1, cw)
	rendered := m.renderLineChunk(layout[0], cw, nil, -1, -1, false)

	if strings.Contains(rendered, staleColor) {
		t.Errorf("stale out-of-range span should be skipped entirely, rendered = %q", rendered)
	}
}

// TestReparseHighlightStampsIncreasingSeq verifies each reparseHighlight
// call bumps Model.hlSeq (shared across value-copies via its pointer — see
// hlSeq's doc comment) and stamps the new value into the highlightMsg its
// returned Cmd produces.
func TestReparseHighlightStampsIncreasingSeq(t *testing.T) {
	m := newAutoPairGoTestModel(t, "package main\n")
	m.hlSeq = new(uint64)

	cmd1 := m.reparseHighlight()
	if cmd1 == nil {
		t.Fatal("reparseHighlight() = nil Cmd, want non-nil")
	}
	msg1, ok := cmd1().(highlightMsg)
	if !ok {
		t.Fatalf("cmd1() = %T, want highlightMsg", cmd1())
	}

	cmd2 := m.reparseHighlight()
	msg2, ok := cmd2().(highlightMsg)
	if !ok {
		t.Fatalf("cmd2() = %T, want highlightMsg", cmd2())
	}

	if msg1.seq == 0 {
		t.Error("first highlightMsg.seq = 0, want a positive sequence number")
	}
	if msg2.seq <= msg1.seq {
		t.Errorf("second highlightMsg.seq = %d, want it greater than the first's %d", msg2.seq, msg1.seq)
	}
}

// TestHighlightMsgDiscardsStaleSequence verifies the highlightMsg handler
// discards a result whose seq no longer matches Model.hlSeq's current
// value — i.e. a highlightMsg superseded by a later reparseHighlight call
// arriving after that later one already landed.
func TestHighlightMsgDiscardsStaleSequence(t *testing.T) {
	m := newTestModel("")
	m.hlSeq = new(uint64)
	*m.hlSeq = 2 // as if two reparseHighlight requests have been issued so far

	current := highlight.LineSpans{0: {{StartCol: 0, EndCol: 1, ANSI: "current"}}}
	stale := highlight.LineSpans{0: {{StartCol: 0, EndCol: 1, ANSI: "stale"}}}

	// The current (seq 2) result arrives and is applied.
	m2, _ := m.Update(highlightMsg{spans: current, seq: 2})
	got := m2.(Model)
	if !reflect.DeepEqual(got.hlSpans, current) {
		t.Fatalf("setup: current-seq highlightMsg wasn't applied, hlSpans = %v", got.hlSpans)
	}

	// An older (seq 1) result arriving late must be discarded, not
	// overwrite the current spans already applied.
	m3, _ := got.Update(highlightMsg{spans: stale, seq: 1})
	got3 := m3.(Model)
	if !reflect.DeepEqual(got3.hlSpans, current) {
		t.Errorf("stale highlightMsg (seq 1) overwrote current spans (seq 2): hlSpans = %v", got3.hlSpans)
	}
}

// TestSetFileTypeAutoInvalidatesOlderInFlightHighlight is an end-to-end
// regression test for the scenario the sequencing exists to prevent: a
// slow ":set ft=<lang>" reparse still in flight when ":set ft=auto"
// supersedes it must not have its late-arriving result overwrite the
// newer, auto-detected highlighter's spans.
func TestSetFileTypeAutoInvalidatesOlderInFlightHighlight(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	if highlight.New("test.go") == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}
	m := newTestModel("def foo():\n    pass\n")
	m.filePath = "test.go"
	m.cfg = &config.Config{}
	m.hlSeq = new(uint64)

	m.cmdBuf = "set ft=py"
	m1, cmd1 := m.executeCommand()
	got1 := m1.(Model)
	if cmd1 == nil {
		t.Fatal("set ft=py: reparseHighlight cmd = nil")
	}
	staleMsg, ok := cmd1().(highlightMsg)
	if !ok {
		t.Fatalf("set ft=py: cmd1() = %T, want highlightMsg", cmd1())
	}

	got1.cmdBuf = "set ft=auto"
	m2, cmd2 := got1.executeCommand()
	got2 := m2.(Model)
	if cmd2 == nil {
		t.Fatal("set ft=auto: reparseHighlight cmd = nil")
	}
	currentMsg, ok := cmd2().(highlightMsg)
	if !ok {
		t.Fatalf("set ft=auto: cmd2() = %T, want highlightMsg", cmd2())
	}

	if staleMsg.seq >= currentMsg.seq {
		t.Fatalf("set ft=py's request seq (%d) should be older than set ft=auto's (%d)", staleMsg.seq, currentMsg.seq)
	}

	// Apply in real (network/scheduling) arrival order: the newer request's
	// result lands first, then the older, slower one arrives late.
	m3, _ := got2.Update(currentMsg)
	afterCurrent := m3.(Model)
	m4, _ := afterCurrent.Update(staleMsg)
	afterStale := m4.(Model)

	if !reflect.DeepEqual(afterStale.hlSpans, afterCurrent.hlSpans) {
		t.Error("the stale \":set ft=py\" highlight result overwrote \":set ft=auto\"'s spans")
	}
}

// TestScheduleLSPOverlayRefreshDebounceCoalescesRapidEdits verifies the
// lspOverlaySeq token: a burst of edits (the common case while typing)
// schedules a refresh repeatedly, but only the LATEST scheduled refresh
// should actually fire — earlier ones must be recognized as stale by the
// lspOverlayRefreshMsg handler and ignored, so rapid typing coalesces into
// one LSP round trip instead of one per keystroke.
func TestScheduleLSPOverlayRefreshDebounceCoalescesRapidEdits(t *testing.T) {
	m := newTestModel("line one\n")
	m.rpc = &RPC{}
	m.cfg = &config.Config{SemanticTokens: true, InlayHints: true}

	m, _ = m.scheduleLSPOverlayRefresh()
	firstSeq := m.lspOverlaySeq
	m, _ = m.scheduleLSPOverlayRefresh()
	secondSeq := m.lspOverlaySeq

	if firstSeq == secondSeq {
		t.Fatal("setup: expected two successive schedules to produce different seq tokens")
	}

	// The stale (first) refresh message must be ignored.
	_, cmd := m.Update(lspOverlayRefreshMsg{seq: firstSeq})
	if cmd != nil {
		t.Error("a stale lspOverlayRefreshMsg should not trigger a re-fetch")
	}

	// The current (second/latest) refresh message must be accepted (non-nil
	// Cmd — the actual fetch is async and RPC-backed, not asserted here).
	_, cmd2 := m.Update(lspOverlayRefreshMsg{seq: secondSeq})
	if cmd2 == nil {
		t.Error("the current lspOverlayRefreshMsg should trigger a re-fetch")
	}
}

// TestShiftLSPOverlayLinesOnInsert is a regression test for a reported bug:
// after leaving stale cache entries untouched on edit (rather than clearing
// them), every line AFTER a line-count-changing edit still went white until
// the debounced refresh landed. Root cause: leaving the data at its OLD line
// numbers isn't enough — the renderer looks up the NEW line numbers, finds
// nothing there, and that's visually indistinguishable from having cleared
// it. Cached data at or after the edit point must be re-keyed by the line
// delta, not merely preserved in place.
func TestShiftLSPOverlayLinesOnInsert(t *testing.T) {
	m := newTestModel("line one\nline two\nline three\n")
	m.semanticSpans = highlight.LineSpans{
		0: {{StartCol: 0, EndCol: 4, ANSI: "a"}},
		1: {{StartCol: 0, EndCol: 4, ANSI: "b"}},
		2: {{StartCol: 0, EndCol: 4, ANSI: "c"}},
	}
	m.inlayHints = []ClientInlayHint{{Line: 1, Col: 4, Label: "x"}, {Line: 2, Col: 4, Label: "y"}}

	// Insert a newline at the start of line 1 — everything from line 1
	// onward shifts down by one.
	m2 := m.shiftLSPOverlayLines(1, 1)

	if len(m2.semanticSpans[0]) != 1 {
		t.Errorf("line 0 (before the edit) = %v, want unchanged", m2.semanticSpans[0])
	}
	if len(m2.semanticSpans[1]) != 0 {
		t.Errorf("line 1 (the old key) = %v, want empty — its data should have moved to line 2", m2.semanticSpans[1])
	}
	if len(m2.semanticSpans[2]) != 1 || m2.semanticSpans[2][0].ANSI != "b" {
		t.Errorf("line 2 = %v, want old line 1's span (\"b\") shifted here", m2.semanticSpans[2])
	}
	if len(m2.semanticSpans[3]) != 1 || m2.semanticSpans[3][0].ANSI != "c" {
		t.Errorf("line 3 = %v, want old line 2's span (\"c\") shifted here", m2.semanticSpans[3])
	}

	wantHints := map[int]bool{2: false, 3: false}
	for _, h := range m2.inlayHints {
		if _, ok := wantHints[h.Line]; !ok {
			t.Errorf("unexpected inlay hint at shifted line %d", h.Line)
			continue
		}
		wantHints[h.Line] = true
	}
	for line, found := range wantHints {
		if !found {
			t.Errorf("expected an inlay hint shifted to line %d", line)
		}
	}
}

// TestShiftLSPOverlayLinesOnDelete verifies a multi-line delete drops cached
// data for the deleted lines and shifts data after them up, rather than
// leaving orphaned entries at now-nonexistent or colliding line numbers.
func TestShiftLSPOverlayLinesOnDelete(t *testing.T) {
	m := newTestModel("a\nb\nc\nd\ne\n")
	m.semanticSpans = highlight.LineSpans{
		1: {{StartCol: 0, EndCol: 1, ANSI: "b"}}, // within the deleted range [1,4)
		3: {{StartCol: 0, EndCol: 1, ANSI: "d"}}, // within the deleted range
		4: {{StartCol: 0, EndCol: 1, ANSI: "e"}}, // after the deleted range
	}

	// Delete lines [1,4) — a 3-line deletion, delta = -3, atLine = 1. Old line
	// 4 ("e", after the deleted range) shifts to new line 1 (4 + -3 = 1).
	m2 := m.shiftLSPOverlayLines(1, -3)

	if len(m2.semanticSpans[3]) != 0 {
		t.Errorf("old line 3's key (within the deleted range) should be empty, got %v", m2.semanticSpans[3])
	}
	got := m2.semanticSpans[1]
	if len(got) != 1 || got[0].ANSI != "e" {
		t.Errorf("semanticSpans[1] = %v, want old line 4's span (\"e\") shifted to the new line 1", got)
	}
}
