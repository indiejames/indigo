package format

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestRunExternalHangingProcessTimesOut is a regression test: an external
// formatter that hangs past its timeout must be killed and surfaced as an
// error rather than blocking runExternal (and its caller) forever.
// externalFormatTimeout is temporarily lowered so the test doesn't have to
// wait out the real 10s production timeout.
func TestRunExternalHangingProcessTimesOut(t *testing.T) {
	orig := externalFormatTimeout
	externalFormatTimeout = 100 * time.Millisecond
	defer func() { externalFormatTimeout = orig }()

	start := time.Now()
	_, _, err := runExternal(makeFC("sh", "-c", "sleep 30"), "/tmp/test.go", "content")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a formatter that hangs past its timeout, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("runExternal took %v to return, want well under the 30s the process would otherwise sleep for", elapsed)
	}
}

// TestRunExternalEmptyOutputNonEmptyInputErrors is a regression test: a
// formatter that exits 0 but produces no output (misconfigured flag, silent
// crash that still returns 0) must not be treated as "successfully deleted
// all the content" — it should error and leave the original content
// untouched instead of blanking the buffer/file.
func TestRunExternalEmptyOutputNonEmptyInputErrors(t *testing.T) {
	content := "package main\n"
	got, changed, err := runExternal(makeFC("true"), "/tmp/test.go", content)
	if err == nil {
		t.Fatal("expected error from formatter producing empty output for non-empty input, got nil")
	}
	if changed {
		t.Error("changed should be false when the empty-output guard rejects the result")
	}
	if got != content {
		t.Errorf("got %q, want original content %q preserved on rejection", got, content)
	}
}

// TestRunExternalWhitespaceOnlyOutputErrors extends the empty-output guard
// to whitespace-only output — a formatter that emits only spaces/tabs/
// newlines for non-empty input is just as clearly broken as one producing
// nothing at all, and should be rejected the same way.
func TestRunExternalWhitespaceOnlyOutputErrors(t *testing.T) {
	content := "package main\n"
	got, changed, err := runExternal(makeFC("sh", "-c", "printf ' \\n\\t \\n'"), "/tmp/test.go", content)
	if err == nil {
		t.Fatal("expected error from formatter producing whitespace-only output for non-empty input, got nil")
	}
	if changed {
		t.Error("changed should be false when the empty-output guard rejects the result")
	}
	if got != content {
		t.Errorf("got %q, want original content %q preserved on rejection", got, content)
	}
}

// TestRunExternalEmptyInputEmptyOutputOK verifies the guard doesn't false
// positive on the legitimate case of formatting an already-empty file.
func TestRunExternalEmptyInputEmptyOutputOK(t *testing.T) {
	got, changed, err := runExternal(makeFC("cat"), "/tmp/test.go", "")
	if err != nil {
		t.Fatalf("runExternal(cat, empty input): %v", err)
	}
	if changed {
		t.Error("changed should be false for empty→empty")
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
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

// TestFormatPropagatesLSPError is a regression test: a genuine LSP
// formatting failure (timeout, malformed response, ...) must surface as
// itself, not get silently reported as ErrNoFormatter — the caller uses
// ErrNoFormatter to show "No formatter available", which would be
// misleading when a formatter is available but just failed.
func TestFormatPropagatesLSPError(t *testing.T) {
	wantErr := errors.New("lsp request timed out")
	fl := &fakeLSP{err: wantErr}
	m := &Manager{lsp: fl, cfg: &config.Config{}}

	_, changed, err := m.Format("/tmp/foo.xyz", "content\n")
	if err != wantErr {
		t.Errorf("Format error = %v, want %v", err, wantErr)
	}
	if changed {
		t.Error("changed should be false when the LSP formatter errored")
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

// ---- monorepo node_modules/.bin resolution ----

// TestFormatFindsFormatterInNestedPackageNodeModules is a regression test
// for a live bug report: a monorepo package with its own non-hoisted
// node_modules (a formatter installed only in
// "services/cron-service/node_modules/.bin/", not at the workspace root)
// used to never be found — NewManager only ever checked
// "<workDir>/node_modules/.bin/" once at startup. Format must resolve the
// nested binary by walking up from the file's own directory (see
// internal/localbin.Resolve), matching this exact real-world path shape.
func TestFormatFindsFormatterInNestedPackageNodeModules(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "services", "cron-service")
	binDir := filepath.Join(pkgDir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake "prettier" that uppercases stdin, so a changed/transformed
	// result proves this specific binary actually ran.
	script := "#!/bin/sh\ntr a-z A-Z\n"
	fakePrettier := filepath.Join(binDir, "prettier")
	if err := os.WriteFile(fakePrettier, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// Constructed directly (not via NewManager) so autoFmts stays empty
	// regardless of whether the test machine happens to have a real
	// prettier on PATH — this test is specifically about the
	// node_modules/.bin fallback, not PATH detection.
	m := &Manager{
		lsp:     &fakeLSP{},
		cfg:     &config.Config{},
		workDir: root,
	}

	filePath := filepath.Join(pkgDir, "app", "cronjobs", "update-user-work.ts")
	got, changed, err := m.Format(filePath, "hello\n")
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !changed || got != "HELLO\n" {
		t.Errorf("Format = (%q, %v), want (%q, true) from the nested package's prettier", got, changed, "HELLO\n")
	}
}

// TestFormatPrefersNodeModulesBinOverLSPWhenNotOnPath verifies the
// resolved node_modules/.bin formatter wins over the generic LSP fallback,
// the same priority PATH-detected auto formatters already have.
func TestFormatPrefersNodeModulesBinOverLSPWhenNotOnPath(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "prettier"), []byte("#!/bin/sh\ntr a-z A-Z\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	fl := &fakeLSP{formatted: "LSP OUTPUT\n", changed: true}
	m := &Manager{lsp: fl, cfg: &config.Config{}, workDir: root}

	got, changed, err := m.Format(filepath.Join(root, "foo.ts"), "hello\n")
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !changed || got != "HELLO\n" {
		t.Errorf("Format = (%q, %v), want (%q, true) from node_modules/.bin, not the LSP fallback", got, changed, "HELLO\n")
	}
	if fl.called {
		t.Error("LSP formatter should not have been called — node_modules/.bin should win")
	}
}
