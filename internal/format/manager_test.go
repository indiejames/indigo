package format

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lsp"
)

// ---- expandPath ----

func TestExpandPathTilde(t *testing.T) {
	got := expandPath("~/bin/gofmt")
	if got == "~/bin/gofmt" {
		t.Error("expandPath did not expand ~/")
	}
	if len(got) < 3 {
		t.Errorf("expandPath returned suspiciously short path: %q", got)
	}
}

func TestExpandPathAbsolute(t *testing.T) {
	p := "/usr/local/bin/gofmt"
	if got := expandPath(p); got != p {
		t.Errorf("expandPath(%q) = %q, want unchanged", p, got)
	}
}

func TestExpandPathPlain(t *testing.T) {
	if got := expandPath("gofmt"); got != "gofmt" {
		t.Errorf("expandPath(plain name) = %q, want gofmt", got)
	}
}

// ---- expandArgs ----

func TestExpandArgsFilePlaceholder(t *testing.T) {
	args := []string{"--stdin-filepath", "{file}", "--other"}
	got := expandArgs(args, "/tmp/foo.ts")
	want := []string{"--stdin-filepath", "/tmp/foo.ts", "--other"}
	if len(got) != len(want) {
		t.Fatalf("expandArgs len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("expandArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpandArgsNoPlaceholder(t *testing.T) {
	args := []string{"-q", "-"}
	got := expandArgs(args, "/tmp/foo.py")
	for i, a := range got {
		if a != args[i] {
			t.Errorf("expandArgs[%d] = %q, want %q (no placeholder)", i, a, args[i])
		}
	}
}

func TestExpandArgsNil(t *testing.T) {
	if got := expandArgs(nil, "/tmp/foo.go"); got != nil {
		t.Errorf("expandArgs(nil) = %v, want nil", got)
	}
}

// ---- matchesExt ----

func TestMatchesExt(t *testing.T) {
	exts := []string{"go", "mod"}
	if !matchesExt(exts, "go") {
		t.Error("matchesExt should match go")
	}
	if !matchesExt(exts, "mod") {
		t.Error("matchesExt should match mod")
	}
	if matchesExt(exts, "rs") {
		t.Error("matchesExt should not match rs")
	}
	if matchesExt(exts, "") {
		t.Error("matchesExt should not match empty string")
	}
}

// ---- runExternal ----

func makeFC(cmd string, args ...string) config.FormatterConfig {
	return config.FormatterConfig{Command: cmd, Args: args}
}

// TestRunExternalPassthrough uses 'cat' to verify stdin→stdout round-trip.
func TestRunExternalPassthrough(t *testing.T) {
	content := "hello\nworld\n"
	got, changed, err := runExternal(makeFC("cat"), "/tmp/test.go", content)
	if err != nil {
		t.Fatalf("runExternal(cat): %v", err)
	}
	if got != content {
		t.Errorf("cat: got %q, want %q", got, content)
	}
	if changed {
		t.Error("cat: changed should be false (content identical)")
	}
}

// TestRunExternalFormatsContent uses tr to verify transformed output is returned.
func TestRunExternalFormatsContent(t *testing.T) {
	got, changed, err := runExternal(makeFC("tr", "a-z", "A-Z"), "/tmp/test.go", "hello\n")
	if err != nil {
		t.Fatalf("runExternal(tr): %v", err)
	}
	if got != "HELLO\n" {
		t.Errorf("tr: got %q, want %q", got, "HELLO\n")
	}
	if !changed {
		t.Error("tr: changed should be true")
	}
}

// TestRunExternalNonZeroExit verifies that a failing formatter returns an error.
func TestRunExternalNonZeroExit(t *testing.T) {
	_, _, err := runExternal(makeFC("false"), "/tmp/test.go", "content")
	if err == nil {
		t.Error("expected error from formatter with non-zero exit, got nil")
	}
}

// ---- Manager.Format ----

// fakeLSP is a stub LSPFormatter that records the options it was called
// with and returns a canned result.
type fakeLSP struct {
	called    bool
	gotOpts   lsp.FormattingOptions
	formatted string
	changed   bool
	err       error
}

func (f *fakeLSP) Format(path, content string, opts lsp.FormattingOptions) (string, bool, error) {
	f.called = true
	f.gotOpts = opts
	if f.err != nil {
		return content, false, f.err
	}
	return f.formatted, f.changed, nil
}

// TestFormatPrefersAutoFormatterOverLSP is a regression test: a dedicated
// auto-detected formatter (e.g. prettier) must win over generic LSP
// formatting when both are available for the same extension, since it
// honors project-local config files the LSP formatter may ignore.
func TestFormatPrefersAutoFormatterOverLSP(t *testing.T) {
	fl := &fakeLSP{formatted: "LSP OUTPUT\n", changed: true}
	auto := makeFC("tr", "a-z", "A-Z")
	auto.Extensions = []string{"ts"}
	m := &Manager{
		lsp:      fl,
		cfg:      &config.Config{},
		autoFmts: []config.FormatterConfig{auto},
	}

	got, changed, err := m.Format("/tmp/foo.ts", "hello\n")
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !changed || got != "HELLO\n" {
		t.Errorf("Format = (%q, %v), want (%q, true) from the auto formatter", got, changed, "HELLO\n")
	}
	if fl.called {
		t.Error("LSP formatter should not be called when an auto formatter matches")
	}
}

// TestFormatFallsBackToLSPWhenNoAutoFormatter verifies the LSP formatter is
// still used for extensions with no configured or auto-detected formatter.
func TestFormatFallsBackToLSPWhenNoAutoFormatter(t *testing.T) {
	fl := &fakeLSP{formatted: "formatted\n", changed: true}
	m := &Manager{lsp: fl, cfg: &config.Config{}}

	got, changed, err := m.Format("/tmp/foo.rs", "content\n")
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !changed || got != "formatted\n" {
		t.Errorf("Format = (%q, %v), want (%q, true)", got, changed, "formatted\n")
	}
	if !fl.called {
		t.Error("LSP formatter should be called when no auto formatter matches")
	}
}

// TestFormatReturnsErrNoFormatterWhenNothingAvailable verifies the sentinel
// error when neither an external nor an LSP formatter can handle the file.
func TestFormatReturnsErrNoFormatterWhenNothingAvailable(t *testing.T) {
	fl := &fakeLSP{changed: false}
	m := &Manager{lsp: fl, cfg: &config.Config{}}

	_, _, err := m.Format("/tmp/foo.xyz", "content\n")
	if err != ErrNoFormatter {
		t.Errorf("Format error = %v, want ErrNoFormatter", err)
	}
}

// ---- lspFormattingOptions ----

func TestLSPFormattingOptionsUsesDetectedIndent(t *testing.T) {
	m := &Manager{cfg: &config.Config{}}
	opts := m.lspFormattingOptions("go", "func foo() {\n\tbar()\n}\n")
	if opts.InsertSpaces {
		t.Errorf("InsertSpaces = true, want false for a tab-indented file")
	}
}

func TestLSPFormattingOptionsFallsBackToConfiguredExtDefault(t *testing.T) {
	// No content signal (single line, nothing indented) → falls back to the
	// per-extension default, which is 2-space for ts.
	m := &Manager{cfg: &config.Config{}}
	opts := m.lspFormattingOptions("ts", "const x = 1;\n")
	if !opts.InsertSpaces || opts.TabSize != 2 {
		t.Errorf("opts = %+v, want {TabSize:2 InsertSpaces:true}", opts)
	}
}

func TestLSPFormattingOptionsDetectedContentOverridesConfigDefault(t *testing.T) {
	// File already uses 4-space indentation even though ts defaults to 2:
	// the file's own convention should win.
	m := &Manager{cfg: &config.Config{}}
	opts := m.lspFormattingOptions("ts", "function foo() {\n    bar();\n}\n")
	if !opts.InsertSpaces || opts.TabSize != 4 {
		t.Errorf("opts = %+v, want {TabSize:4 InsertSpaces:true}", opts)
	}
}
