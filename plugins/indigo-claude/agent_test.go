package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/document"
)

// extractSelection must mirror the editor's selection semantics: ranges in
// document order, end column inclusive, IsLine selecting whole lines.
func TestExtractSelection(t *testing.T) {
	content := "alpha\nbravo charlie\ndelta\necho"
	cases := []struct {
		name string
		sel  client.ActiveSelection
		want string
	}{
		{"single line partial",
			client.ActiveSelection{StartLine: 1, StartCol: 6, EndLine: 1, EndCol: 12}, "charlie"},
		{"multi line",
			client.ActiveSelection{StartLine: 1, StartCol: 6, EndLine: 2, EndCol: 4}, "charlie\ndelta"},
		{"line-wise",
			client.ActiveSelection{StartLine: 1, EndLine: 2, IsLine: true}, "bravo charlie\ndelta"},
		{"whole first line char-wise",
			client.ActiveSelection{StartLine: 0, StartCol: 0, EndLine: 0, EndCol: 4}, "alpha"},
		{"end col past line end clamps",
			client.ActiveSelection{StartLine: 3, StartCol: 0, EndLine: 3, EndCol: 99}, "echo"},
		{"start line past EOF",
			client.ActiveSelection{StartLine: 9, EndLine: 9}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractSelection(content, c.sel); got != c.want {
				t.Errorf("extractSelection(%+v) = %q, want %q", c.sel, got, c.want)
			}
		})
	}
}

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

// Regression test: SGR mouse escape sequences that leak past the terminal
// parser during fast wheel scrolling must not end up in the input box.
func TestStripMouseArtifacts(t *testing.T) {
	cases := []struct{ in, want string }{
		// The exact leak reported: a burst of wheel-down events.
		{"<65;70;21M<65;69;18M<65;62;24M<65;62;24M<65;62;24M", ""},
		{"[<64;10;5M", ""},          // with leading [ when ESC alone was eaten
		{"<65;70;21m", ""},          // release variant (lowercase m)
		{"abc<65;70;21Mdef", "abcdef"}, // mixed with real typed text
		{"hello world", "hello world"}, // plain text untouched
		{"a < b; c > d", "a < b; c > d"},
	}
	for _, c := range cases {
		if got := stripMouseArtifacts(c.in); got != c.want {
			t.Errorf("stripMouseArtifacts(%q) = %q, want %q", c.in, got, c.want)
		}
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
