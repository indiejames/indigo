package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// Regression test for the off-by-one insert bug: text inserted "at line N"
// must become line N (1-based), not land a line below.
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

func TestClassifyAgentError(t *testing.T) {
	cases := []struct {
		in       string
		wantPart string // "" = expect unclassified
	}{
		{"Claude AI usage limit reached|1789000000", "resets at"},
		{"Claude AI usage limit reached", "Usage limit reached"},
		{`{"type":"rate_limit_error","message":"..."}`, "Rate limited"},
		{"API 529: Overloaded", "overloaded"},
		{"Your credit balance is too low", "credit balance"},
		{"401 unauthorized", "Authentication failed"},
		{"dial tcp: lookup api.anthropic.com: no such host", "Network error"},
		{"something completely different", ""},
	}
	for _, c := range cases {
		got := classifyAgentError(c.in)
		if c.wantPart == "" {
			if got != "" {
				t.Errorf("classifyAgentError(%q) = %q, want unclassified", c.in, got)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(got), strings.ToLower(c.wantPart)) {
			t.Errorf("classifyAgentError(%q) = %q, want it to mention %q", c.in, got, c.wantPart)
		}
	}
}

// Regression guard for the snippet's line indexing: line is 0-based in, the
// marker and printed numbers are 1-based out. (The live-buffer RPC path needs
// a server; this covers the disk fallback used when rpc is nil.)
func TestBufferSnippetMarksCursorLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("alpha\nbravo\ncharlie\n"), 0644); err != nil {
		t.Fatal(err)
	}
	snip := bufferSnippet(nil, path, 1, 1) // cursor on "bravo" (0-indexed 1)
	if !strings.Contains(snip, "▶    2  bravo") {
		t.Errorf("snippet does not mark line 2/bravo:\n%s", snip)
	}
	if strings.Contains(snip, "▶    1") || strings.Contains(snip, "▶    3") {
		t.Errorf("snippet marks the wrong line:\n%s", snip)
	}
}

func TestFmtTokens(t *testing.T) {
	if got := fmtTokens(512); got != "512" {
		t.Errorf("fmtTokens(512) = %q", got)
	}
	if got := fmtTokens(34200); got != "34.2k" {
		t.Errorf("fmtTokens(34200) = %q", got)
	}
}
