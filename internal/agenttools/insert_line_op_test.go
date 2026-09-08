package agenttools

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// Moved here with insertLineOp when the tool layer was split out of the
// indigo-claude plugin.
func TestInsertLineOp(t *testing.T) {
	cases := []struct {
		name    string
		content string
		line    int
		text    string
		want    string
	}{
		{"middle", "a\nb\nc", 2, "X", "a\nX\nb\nc"},
		{"first line", "a\nb", 1, "X", "X\na\nb"},
		{"cursor line 26 scenario", "l1\nl2\nl3", 3, "// comment", "l1\nl2\n// comment\nl3"},
		{"past end no trailing newline", "a\nb", 3, "X", "a\nb\nX"},
		{"clamped below 1", "a\nb", 0, "X", "X\na\nb"},
		{"multi-line insert", "a\nb", 2, "X\nY", "a\nX\nY\nb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			buf := document.New("t.txt", c.content)
			buf.Apply(insertLineOp(c.content, c.text, c.line))
			if got := buf.Content(); got != c.want {
				t.Errorf("insertLineOp(%q, %q, %d): got %q, want %q",
					c.content, c.text, c.line, got, c.want)
			}
		})
	}
}
