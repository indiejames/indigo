package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestBuildPermPopupUniformWidthWithTabs(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 100, 40

	m.pendingPerm = &permissionRequestMsg{
		file: "main.go",
		edits: []editSpec{{
			path:    "main.go",
			oldText: "func foo() {\n\treturn\n}",
			newText: "func foo() {\n\tif true {\n\t\treturn\n\t}\n}",
		}},
	}

	for _, choice := range []bool{false, true} {
		m.permChoice = choice
		lines := m.buildPermPopup()
		if len(lines) == 0 {
			t.Fatal("buildPermPopup() returned no lines")
		}
		want := lipgloss.Width(lines[0])
		for i, l := range lines {
			if got := lipgloss.Width(l); got != want {
				t.Errorf("permChoice=%v: line %d width = %d, want %d (uniform border)\nline: %q", choice, i, got, want, l)
			}
		}
	}
}

func TestBuildPermPopupCapsHeightForLargeDiff(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 100, 20 // short terminal

	var newText strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&newText, "line %d\n", i)
	}
	m.pendingPerm = &permissionRequestMsg{
		file:  "big.go",
		edits: []editSpec{{path: "big.go", newText: newText.String()}},
	}

	lines := m.buildPermPopup()

	// Must fit within the conversation area above the input box.
	if len(lines) > m.convHeight() {
		t.Errorf("popup has %d lines, taller than convHeight() = %d — would push the input off-screen", len(lines), m.convHeight())
	}
	// The footer (approve/reject) must survive the cap.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "approve") || !strings.Contains(joined, "reject") {
		t.Errorf("approve/reject footer missing from capped popup:\n%s", joined)
	}
	if !strings.Contains(joined, "more line") {
		t.Errorf("expected a truncation indicator for the 200-line diff, got:\n%s", joined)
	}
}

func TestExpandTabs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"no tabs", "no tabs"},
		{"\tfoo", "    foo"},
		{"a\tb", "a   b"},
		{"ab\tc", "ab  c"},
		{"abcd\te", "abcd    e"},
	}
	for _, c := range cases {
		if got := expandTabs(c.in); got != c.want {
			t.Errorf("expandTabs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestThinkingWordRotatesOverTime(t *testing.T) {
	if len(thinkingWords) < 2 {
		t.Fatal("need at least 2 thinking words to test rotation")
	}

	if got := thinkingWord(time.Time{}); got != thinkingWords[0] {
		t.Errorf("thinkingWord(zero time) = %q, want %q", got, thinkingWords[0])
	}

	now := time.Now()
	first := thinkingWord(now)
	if first != thinkingWords[0] {
		t.Errorf("thinkingWord(just started) = %q, want %q", first, thinkingWords[0])
	}

	// Simulate the turn having started long enough ago to have rotated at
	// least once, by backdating "since" rather than sleeping in the test.
	backdated := now.Add(-3 * time.Second)
	second := thinkingWord(backdated)
	if second == first {
		t.Errorf("thinkingWord after 3s = %q, expected it to have rotated past %q", second, first)
	}
}
