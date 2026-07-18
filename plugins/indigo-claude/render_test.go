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
