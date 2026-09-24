package server

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// A workspace replacement's text enters the buffer in the server, so the
// server normalizes it: the buffer holds "\n" only.
func TestWorkspaceEditNormalizesReplacementLineEndings(t *testing.T) {
	entry := &bufferEntry{buf: document.New("/w/a.txt", "foo bar\n")}
	applied, skipped := applyWorkspaceEditsToBuffer(entry, 1, []workspaceEditItem{
		{line: 0, col: 4, oldText: "bar", newText: "baz\r\nqux"},
	})
	if applied != 1 || len(skipped) != 0 {
		t.Fatalf("applied=%d skipped=%v", applied, skipped)
	}
	if got := entry.buf.Content(); strings.Contains(got, "\r") || got != "foo baz\nqux\n" {
		t.Errorf("buffer = %q, want %q", got, "foo baz\nqux\n")
	}
}
