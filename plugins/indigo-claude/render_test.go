package main

import (
	"fmt"
	"strings"
	"testing"
)

// TestRenderConversationDoesNotClipTailWhenPinned reproduces the bug where,
// once the conversation is long enough that the leading user message gets
// pinned at the top of the view, the last few lines of the most recent
// content (however tall the pinned bubble is) were silently dropped instead
// of the view staying anchored to the true bottom.
func TestRenderConversationDoesNotClipTailWhenPinned(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width = 40
	m.height = 10
	m.collapsedMsgs = map[int]bool{}

	m.conv = []ConvMsg{{Role: RoleUser, Content: "question"}}
	for i := 0; i < 30; i++ {
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf("line %d", i)})
	}

	out := m.renderConversation()
	if !strings.Contains(out, "line 29") {
		t.Errorf("renderConversation() at scroll=0 clipped the tail of the most recent content; \"line 29\" (the last line) is missing.\noutput:\n%s", out)
	}
}

// TestWrapInputDisplayWordWraps checks that a long logical line is broken
// into multiple display rows at word boundaries, rather than overflowing a
// single row (the old behavior scrolled such lines horizontally instead).
func TestWrapInputDisplayWordWraps(t *testing.T) {
	input := []rune("the quick brown fox jumps over the lazy dog")
	lines, _, _ := wrapInputDisplay(input, 0, 10)

	if len(lines) < 2 {
		t.Fatalf("expected input to wrap across multiple rows, got %d row(s): %v", len(lines), lines)
	}
	for _, l := range lines {
		if len(l) > 10 {
			t.Errorf("row %q exceeds width 10", string(l))
		}
	}

	// Every non-space rune of the original input must appear in the wrapped
	// output, in order, so no text is silently dropped by the wrap (wrapping
	// may fold a trailing space at a break point onto the prior row).
	var rebuilt []rune
	for _, l := range lines {
		rebuilt = append(rebuilt, l...)
	}
	strip := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	if strip(string(rebuilt)) != strip(string(input)) {
		t.Errorf("wrapped output lost characters: got %q, want (ignoring spaces) %q", string(rebuilt), string(input))
	}
}

// TestWrapInputDisplayCursorPosition checks that the cursor's display row/col
// tracks correctly across a wrapped line, including at the wrap boundary.
func TestWrapInputDisplayCursorPosition(t *testing.T) {
	input := []rune("the quick brown fox jumps over the lazy dog")
	width := 10

	// Cursor at the very start.
	_, row, col := wrapInputDisplay(input, 0, width)
	if row != 0 || col != 0 {
		t.Errorf("cursor at pos 0: got row=%d col=%d, want row=0 col=0", row, col)
	}

	// Cursor at the very end should land on the last display row.
	lines, row, col := wrapInputDisplay(input, len(input), width)
	lastRow := len(lines) - 1
	if row != lastRow || col != len(lines[lastRow]) {
		t.Errorf("cursor at end: got row=%d col=%d, want row=%d col=%d", row, col, lastRow, len(lines[lastRow]))
	}
}

// TestInputHeightGrowsWithWrappedLines checks that a single long line that
// word-wraps across several rows grows the input box height accordingly.
func TestInputHeightGrowsWithWrappedLines(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width = 20
	m.height = 40

	m.input = []rune("the quick brown fox jumps over the lazy dog")
	m.inputPos = len(m.input)

	h := m.inputHeight()
	if h <= 3 {
		t.Errorf("expected inputHeight() to grow for a long wrapped line, got %d", h)
	}
}
