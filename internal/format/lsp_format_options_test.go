package format

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lsp"
)

// TestLSPFormattingPreservesSpaceAfterFunctionKeyword is a regression test for
// a user report: saving a TypeScript file turned `function ()` into
// `function()` on every save, which failed the project's own lint.
//
// The file's project had no prettier installed, so format-on-save fell through
// to the language server, and tsserver defaults
// insertSpaceAfterFunctionKeywordForAnonymousFunctions to false. VS Code ships
// that setting checked ("Insert space after function keyword for anonymous
// functions"), which is why the same file formatted correctly there and not
// here. indigo now sends it, and this drives a real typescript-language-server
// to prove it arrives and is honoured — the setting travels as an extra key on
// the LSP formatting request's options object, which only works because the
// server reads its own keys out of it.
func TestLSPFormattingPreservesSpaceAfterFunctionKeyword(t *testing.T) {
	if _, err := exec.LookPath("typescript-language-server"); err != nil {
		t.Skip("typescript-language-server not installed")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"),
		[]byte(`{"compilerOptions":{"target":"es2020"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	lspMgr := lsp.NewManager(dir, []lsp.ServerConfig{{
		Extensions: []string{"ts", "tsx"},
		Command:    "typescript-language-server",
		Args:       []string{"--stdio"},
	}})
	t.Cleanup(lspMgr.Shutdown)

	// Built by hand rather than through NewManager so the test always
	// exercises the LSP fallback: NewManager would pick up a prettier on the
	// PATH of whatever machine this runs on and never reach it.
	m := &Manager{lsp: lspMgr, cfg: &config.Config{}}

	// Wait for the server to be able to format at all, using a file whose
	// formatting is not in question. Polling for a real reformat is the only
	// honest readiness signal — a server still loading the project answers a
	// formatting request with no edits, which would otherwise read as "the
	// space was preserved" and pass this test for the wrong reason.
	probe := filepath.Join(dir, "probe.ts")
	const badlySpaced = "const  x   =    1\n"
	if err := os.WriteFile(probe, []byte(badlySpaced), 0o644); err != nil {
		t.Fatal(err)
	}
	lspMgr.DidOpen(probe, badlySpaced)
	deadline := time.Now().Add(30 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		_, changed, err := m.Format(probe, badlySpaced)
		switch {
		case err == nil && changed:
			ready = true
		case errors.Is(err, ErrNoFormatter):
			// How "the formatter ran and made no edits" is reported, which is
			// also what a server still loading the project answers. The only
			// error worth continuing to poll through.
		case err != nil:
			// Anything else — the server failed to start, the request errored —
			// is a real failure. Swallowing it here would spend the full 30s
			// and then t.Skip, quietly reporting a broken test environment as
			// "not applicable".
			t.Fatalf("formatting the readiness probe: %v", err)
		}
		if ready {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		t.Skip("typescript-language-server never became ready to format")
	}

	path := filepath.Join(dir, "a.ts")
	const src = "const f = function () { return 1 }\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	lspMgr.DidOpen(path, src)

	got, changed, err := m.Format(path, src)
	switch {
	case errors.Is(err, ErrNoFormatter):
		// The formatter ran and had nothing to change, which is the outcome
		// under test: Format reports "no formatter" when the LSP fallback
		// returns no edits.
	case err != nil:
		t.Fatalf("Format: %v", err)
	case changed && got != src:
		t.Errorf("Format = %q, want the source unchanged (%q) — the space after the "+
			"`function` keyword was removed, which is what broke the user's lint", got, src)
	}
}

// TestLSPFormattingOptionsCarryConfiguredSettings checks the wiring without a
// language server: the settings resolved for the extension have to actually
// reach the request, and a user block has to be able to override a default
// rather than only add to it.
func TestLSPFormattingOptionsCarryConfiguredSettings(t *testing.T) {
	const key = "insertSpaceAfterFunctionKeywordForAnonymousFunctions"

	m := &Manager{cfg: &config.Config{}}
	opts := m.lspFormattingOptions("ts", "const x = 1\n")
	if got := opts.Extra[key]; got != true {
		t.Errorf("default ts options %v = %v, want true", key, got)
	}

	m = &Manager{cfg: &config.Config{LSPFormat: []config.LSPFormatConfig{{
		Extensions: []string{"ts"},
		Options:    map[string]any{key: false, "insertSpaceBeforeFunctionParenthesis": true},
	}}}}
	opts = m.lspFormattingOptions("ts", "const x = 1\n")
	if got := opts.Extra[key]; got != false {
		t.Errorf("configured %v = %v, want the user's false to override the default", key, got)
	}
	if got := opts.Extra["insertSpaceBeforeFunctionParenthesis"]; got != true {
		t.Errorf("configured insertSpaceBeforeFunctionParenthesis = %v, want true", got)
	}

	// A language with no defaults and no configuration must send exactly what
	// it sent before this existed.
	m = &Manager{cfg: &config.Config{}}
	if opts := m.lspFormattingOptions("go", "package main\n"); opts.Extra != nil {
		t.Errorf("go options carry %v, want nothing extra", opts.Extra)
	}
}
