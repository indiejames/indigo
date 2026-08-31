package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/highlight"
)

func TestParseGrepArgs(t *testing.T) {
	cases := []struct {
		in                        string
		pattern, include, exclude string
	}{
		{"TODO", "TODO", "", ""},
		{"TODO *.go", "TODO", "*.go", ""},
		{"TODO !vendor/", "TODO", "", "vendor/"},
		{"TODO *.go !vendor/", "TODO", "*.go", "vendor/"},
		{"TODO *.go *.ts !vendor/ !**/*_test.go", "TODO", "*.go *.ts", "vendor/ **/*_test.go"},
		{"foo bar", "foo bar", "", ""},
		{"*.go", "", "*.go", ""},
		{"!vendor/", "", "", "vendor/"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		pattern, include, exclude := parseGrepArgs(c.in)
		if pattern != c.pattern || include != c.include || exclude != c.exclude {
			t.Errorf("parseGrepArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, pattern, include, exclude, c.pattern, c.include, c.exclude)
		}
	}
}

// TestExecuteCommandSetFileType is an end-to-end regression test for
// ":set ft=<lang>": it must immediately swap the highlighter AND every
// other file-type-derived thing (indent settings, comment prefix, status
// bar label) over to the requested language — not just syntax highlighting
// — even though filePath itself (and its real extension) never changes.
func TestExecuteCommandSetFileType(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.filePath = "notes.txt" // an extension Python's own settings clearly differ from
	m.cfg = &config.Config{}

	m.cmdBuf = "set ft=py"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", got.mode)
	}
	if got.langOverride != "py" {
		t.Errorf("langOverride = %q, want %q", got.langOverride, "py")
	}
	if got.hlr == nil {
		t.Fatal("hlr = nil, want the Python highlighter")
	}
	if !strings.Contains(got.status, "Python") {
		t.Errorf("status = %q, want it to mention Python", got.status)
	}
	if name := got.effectiveFileTypeName(); name != "Python" {
		t.Errorf("effectiveFileTypeName() = %q, want %q", name, "Python")
	}
	if prefix := got.lineCommentPrefix(); prefix != "#" {
		t.Errorf("lineCommentPrefix() = %q, want %q (Python's, not notes.txt's default)", prefix, "#")
	}
	wantIndent := config.IndentSettings{Style: "spaces", Width: 4}
	if indent := got.effectiveIndentSettings(); indent != wantIndent {
		t.Errorf("effectiveIndentSettings() = %+v, want %+v (Python's, not notes.txt's default)", indent, wantIndent)
	}
}

// TestExecuteCommandSetFileTypeCaseInsensitive verifies ":set ft=PY" resolves
// the same as ":set ft=py".
func TestExecuteCommandSetFileTypeCaseInsensitive(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.cmdBuf = "set ft=PY"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "py" {
		t.Errorf("langOverride = %q, want %q", got.langOverride, "py")
	}
	if got.hlr == nil {
		t.Error("hlr = nil, want the Python highlighter")
	}
}

// TestExecuteCommandSetFileTypeUnknown verifies an unrecognized language
// leaves the buffer's existing highlighter/override untouched and reports
// an error instead of silently clearing highlighting.
func TestExecuteCommandSetFileTypeUnknown(t *testing.T) {
	m := newTestModel("")
	m.filePath = "test.go"
	m.hlr = highlight.New("test.go")
	m.cmdBuf = "set ft=not-a-real-language"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "" {
		t.Errorf("langOverride = %q, want empty (unknown language must not set an override)", got.langOverride)
	}
	if got.hlr != m.hlr {
		t.Error("hlr changed on an unknown file type, want it left untouched")
	}
	if !strings.Contains(got.status, "unknown file type") {
		t.Errorf("status = %q, want it to report the unknown file type", got.status)
	}
}

// TestExecuteCommandSetFileTypeAuto verifies ":set ft=auto" clears a
// previously set override and reverts to filePath-derived language.
func TestExecuteCommandSetFileTypeAuto(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.filePath = "test.go"
	m.langOverride = "py"
	m.hlr = highlight.NewForKey("py")

	m.cmdBuf = "set ft=auto"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "" {
		t.Errorf("langOverride = %q, want empty after :set ft=auto", got.langOverride)
	}
	if name := got.effectiveFileTypeName(); name != "Go" {
		t.Errorf("effectiveFileTypeName() = %q, want %q (back to filePath's own type)", name, "Go")
	}
}
