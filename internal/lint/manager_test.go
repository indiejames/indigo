package lint

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lsp"
)

func newTestManager(lints ...config.LinterConfig) *Manager {
	scanCtx, cancelScan := context.WithCancel(context.Background())
	return &Manager{
		userLints:      lints,
		cached:         make(map[string][]lsp.Diagnostic),
		running:        make(map[string]bool),
		pending:        make(map[string]bool),
		content:        make(map[string]string),
		lastErr:        make(map[string]error),
		activeToken:    make(map[string]uint64),
		workspaceByCmd: make(map[string]map[string][]lsp.Diagnostic),
		workspaceErrs:  make(map[string]error),
		scanCtx:        scanCtx,
		cancelScan:     cancelScan,
	}
}

// runSync performs the runAsync-style token bookkeeping (claim a fresh
// token as the path's activeToken) and then calls run synchronously, for
// tests that want deterministic, goroutine-free behavior identical to what
// RunAsync would produce.
func (m *Manager) runSync(path string, lc config.LinterConfig) {
	m.mu.Lock()
	m.tokenSeq++
	token := m.tokenSeq
	m.activeToken[path] = token
	m.mu.Unlock()
	m.run(path, lc, token)
}

// shJSON is a linter stand-in that needs no external tool installed: it
// shells out to `sh -c` and echoes a canned golangci-lint-shaped JSON blob.
func shJSON(json string) config.LinterConfig {
	return config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "sh",
		Args:       []string{"-c", "echo '" + json + "'"},
		Format:     "golangci-lint-json",
	}
}

func TestManagerRunPopulatesCache(t *testing.T) {
	lc := shJSON(`{"Issues":[{"FromLinter":"x","Text":"boom","Severity":"error","Pos":{"Line":1,"Column":1}}]}`)
	m := newTestManager(lc)

	m.runSync("foo.go", lc) // synchronous, bypassing the "go" in RunAsync

	diags := m.GetDiagnostics("foo.go")
	if len(diags) != 1 {
		t.Fatalf("len(diags) = %d, want 1", len(diags))
	}
	if diags[0].Message != "boom" {
		t.Errorf("message = %q, want boom", diags[0].Message)
	}
}

func TestManagerGetDiagnosticsUnknownPath(t *testing.T) {
	m := newTestManager()
	if diags := m.GetDiagnostics("nope.go"); diags != nil {
		t.Errorf("diags = %v, want nil for an unknown path", diags)
	}
}

func TestManagerRunAsyncCoalescesWhileRunning(t *testing.T) {
	lc := shJSON(`{"Issues":[]}`)
	m := newTestManager(lc)
	m.running["foo.go"] = true

	m.RunAsync("foo.go", "")

	if !m.pending["foo.go"] {
		t.Error("RunAsync while running should set pending, not launch a new run")
	}
	if diags := m.GetDiagnostics("foo.go"); diags != nil {
		t.Errorf("cache should be untouched by a coalesced call, got %v", diags)
	}
}

func TestManagerRunAsyncNoMatchingLinter(t *testing.T) {
	m := newTestManager()    // no linters configured at all
	m.RunAsync("foo.go", "") // must not panic or spawn anything

	if diags := m.GetDiagnostics("foo.go"); diags != nil {
		t.Errorf("diags = %v, want nil when no linter matches", diags)
	}
}

func TestManagerRunUsesStdinContentForStdinLinter(t *testing.T) {
	// Echoes whatever it receives on stdin back as the diagnostic message,
	// so a correct message proves content was actually piped in rather than
	// the (nonexistent) "foo.go" being read off disk.
	lc := config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "sh",
		Args:       []string{"-c", `printf '{"Issues":[{"FromLinter":"x","Text":"%s","Severity":"error","Pos":{"Line":1,"Column":1}}]}' "$(cat)"`},
		Format:     "golangci-lint-json",
		Stdin:      true,
	}
	m := newTestManager(lc)
	m.content["foo.go"] = "boom from stdin"

	m.runSync("foo.go", lc) // synchronous, bypassing the "go" in RunAsync

	diags := m.GetDiagnostics("foo.go")
	if len(diags) != 1 || diags[0].Message != "boom from stdin" {
		t.Fatalf("diags = %+v, want one diagnostic with message %q", diags, "boom from stdin")
	}
}

func TestManagerRunOnEditSkipsDiskLinter(t *testing.T) {
	lc := shJSON(`{"Issues":[{"FromLinter":"x","Text":"should not run","Severity":"error","Pos":{"Line":1,"Column":1}}]}`)
	m := newTestManager(lc) // lc.Stdin is false: a compile-based, disk-reading linter

	m.RunOnEdit("foo.go", "irrelevant content")

	if m.running["foo.go"] {
		t.Error("RunOnEdit should not start a run for a disk-reading linter")
	}
	if diags := m.GetDiagnostics("foo.go"); diags != nil {
		t.Errorf("diags = %v, want nil — RunOnEdit must not run disk-based linters", diags)
	}
}

func TestManagerRunOnEditCoalescesForStdinLinter(t *testing.T) {
	lc := config.LinterConfig{Extensions: []string{"go"}, Command: "sh", Format: "golangci-lint-json", Stdin: true}
	m := newTestManager(lc)
	// A real file: RunOnEdit skips paths with nothing on disk, since a
	// type-aware linter reports that absence rather than analysing the buffer.
	// This test is about coalescing, so it has to get past that guard.
	path := filepath.Join(t.TempDir(), "foo.go")
	if err := os.WriteFile(path, []byte("package p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.running[path] = true

	m.RunOnEdit(path, "latest buffer text")

	if !m.pending[path] {
		t.Error("RunOnEdit while running should set pending, not launch a new run")
	}
	if got := m.content[path]; got != "latest buffer text" {
		t.Errorf("content[%q] = %q, want %q", path, got, "latest buffer text")
	}
}

// TestManagerLastErrorSurfacesFailureWithoutClobberingCache is a regression
// test: a linter that fails (here, a nonexistent command) used to leave
// cached[path] exactly as it was — correct for not blanking working
// diagnostics, but with no way to tell the cache had gone stale. LastError
// must report the failure even though GetDiagnostics keeps returning the
// last good result.
func TestManagerLastErrorSurfacesFailureWithoutClobberingCache(t *testing.T) {
	goodLC := shJSON(`{"Issues":[{"FromLinter":"x","Text":"boom","Severity":"error","Pos":{"Line":1,"Column":1}}]}`)
	m := newTestManager(goodLC)
	m.runSync("foo.go", goodLC)
	if diags := m.GetDiagnostics("foo.go"); len(diags) != 1 {
		t.Fatalf("setup: len(diags) = %d, want 1", len(diags))
	}
	if err := m.LastError("foo.go"); err != nil {
		t.Fatalf("setup: LastError = %v, want nil after a successful run", err)
	}

	failingLC := config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "indigo-lint-command-does-not-exist-xyz",
		Format:     "golangci-lint-json",
	}
	m.runSync("foo.go", failingLC)

	if err := m.LastError("foo.go"); err == nil {
		t.Error("LastError = nil after a failing run, want the run's error")
	}
	if diags := m.GetDiagnostics("foo.go"); len(diags) != 1 {
		t.Errorf("GetDiagnostics after a failed run = %v, want the previous cache left untouched", diags)
	}

	// A subsequent successful run clears the error again.
	m.runSync("foo.go", goodLC)
	if err := m.LastError("foo.go"); err != nil {
		t.Errorf("LastError = %v, want nil after a follow-up successful run", err)
	}
}

// TestManagerForgetPrunesAllState is a regression test: cached/running/
// pending/content/lastErr previously had no way to remove a path's entry at
// all, so every file ever linted across a session accumulated an entry
// forever even after being closed.
func TestManagerForgetPrunesAllState(t *testing.T) {
	m := newTestManager()
	m.cached["foo.go"] = nil
	m.running["foo.go"] = true
	m.pending["foo.go"] = true
	m.content["foo.go"] = "some content"
	m.lastErr["foo.go"] = fmt.Errorf("boom")
	m.activeToken["foo.go"] = 1

	m.Forget("foo.go")

	if _, ok := m.cached["foo.go"]; ok {
		t.Error("cached still has an entry for foo.go after Forget")
	}
	if _, ok := m.running["foo.go"]; ok {
		t.Error("running still has an entry for foo.go after Forget")
	}
	if _, ok := m.pending["foo.go"]; ok {
		t.Error("pending still has an entry for foo.go after Forget")
	}
	if _, ok := m.content["foo.go"]; ok {
		t.Error("content still has an entry for foo.go after Forget")
	}
	if _, ok := m.lastErr["foo.go"]; ok {
		t.Error("lastErr still has an entry for foo.go after Forget")
	}
	if _, ok := m.activeToken["foo.go"]; ok {
		t.Error("activeToken still has an entry for foo.go after Forget")
	}
}

// TestManagerForgetInvalidatesBlockedRunThenNewRunWins is a regression
// test: Forget used to only clear the maps, leaving a run that was already
// in flight free to repopulate cached/lastErr when it eventually completed
// — silently undoing the Forget, or worse, clobbering a newer run's result
// if the path was reopened and relinted before the stale run finished.
// activeToken must make a run's completion a no-op once Forget (or a newer
// run claiming the path) has moved past its token.
func TestManagerForgetInvalidatesBlockedRunThenNewRunWins(t *testing.T) {
	staleLC := shJSON(`{"Issues":[{"FromLinter":"x","Text":"stale","Severity":"error","Pos":{"Line":1,"Column":1}}]}`)
	freshLC := shJSON(`{"Issues":[{"FromLinter":"x","Text":"fresh","Severity":"error","Pos":{"Line":1,"Column":1}}]}`)
	m := newTestManager()

	// A run for foo.go is in flight (claimed a token via the same
	// bookkeeping runAsync does) when the file is closed.
	m.mu.Lock()
	m.running["foo.go"] = true
	m.tokenSeq++
	staleToken := m.tokenSeq
	m.activeToken["foo.go"] = staleToken
	m.mu.Unlock()

	m.Forget("foo.go")

	// The file is reopened and relinted before the stale run had a chance
	// to write back its result — this claims a new, higher token.
	m.mu.Lock()
	m.running["foo.go"] = true
	m.tokenSeq++
	freshToken := m.tokenSeq
	m.activeToken["foo.go"] = freshToken
	m.mu.Unlock()

	// The new run finishes first...
	m.run("foo.go", freshLC, freshToken)
	// ...then the old, now-invalidated run finally completes.
	m.run("foo.go", staleLC, staleToken)

	diags := m.GetDiagnostics("foo.go")
	if len(diags) != 1 || diags[0].Message != "fresh" {
		t.Fatalf("diags = %+v, want the fresh run's single diagnostic — the stale in-flight run should have been discarded", diags)
	}
	if err := m.LastError("foo.go"); err != nil {
		t.Errorf("LastError = %v, want nil (the fresh run succeeded and the stale run's completion should be a no-op)", err)
	}
}

func TestFindLinterUserOverridesAuto(t *testing.T) {
	userLC := config.LinterConfig{Extensions: []string{"go"}, Command: "user-linter", Format: "golangci-lint-json"}
	m := &Manager{
		userLints: []config.LinterConfig{userLC},
		autoLints: []config.LinterConfig{{Extensions: []string{"go"}, Command: "auto-linter", Format: "golangci-lint-json"}},
	}
	got, ok := m.findLinter("foo.go", "go")
	if !ok || got.Command != "user-linter" {
		t.Errorf("findLinter(go) = %+v, want user-linter to take precedence", got)
	}
}

// TestFindLinterFindsLinterInNestedPackageNodeModules is a regression test
// for a live bug report (fixed in format.Manager first, then mirrored
// here): a monorepo package with its own non-hoisted node_modules (a
// linter installed only in "services/cron-service/node_modules/.bin/", not
// at the workspace root) used to never be found — NewManager only ever
// checked "<workDir>/node_modules/.bin/" once at startup. findLinter must
// resolve the nested binary by walking up from the file's own directory
// (see internal/localbin.Resolve).
func TestFindLinterFindsLinterInNestedPackageNodeModules(t *testing.T) {
	root := t.TempDir()
	pkgDir := filepath.Join(root, "services", "cron-service")
	binDir := filepath.Join(pkgDir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeESLint := filepath.Join(binDir, "eslint")
	if err := os.WriteFile(fakeESLint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := newTestManager()
	m.workDir = root

	filePath := filepath.Join(pkgDir, "app", "cronjobs", "update-user-work.ts")
	got, ok := m.findLinter(filePath, "ts")
	if !ok {
		t.Fatal("expected to find the nested package's eslint by walking up")
	}
	if got.Command != fakeESLint {
		t.Errorf("findLinter Command = %q, want the nested binary %q", got.Command, fakeESLint)
	}
}

// TestEffectiveWorkspaceLintersFindsWorkspaceRootNodeModulesBinary is a
// regression test for a gap introduced alongside the node_modules/.bin fix
// above: findLinter resolves a per-file linter under node_modules/.bin, but
// effectiveWorkspaceLinters (used by ScanWorkspace) only ever consulted
// autoLints, which is populated from PATH only. A default linter installed
// solely under "<workDir>/node_modules/.bin/" — not even a monorepo nested
// case, just the plain common one — used to be silently missing from
// workspace-wide scans.
func TestEffectiveWorkspaceLintersFindsWorkspaceRootNodeModulesBinary(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeESLint := filepath.Join(binDir, "eslint")
	if err := os.WriteFile(fakeESLint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	m := newTestManager()
	m.workDir = root

	linters := m.effectiveWorkspaceLinters()
	var found *config.LinterConfig
	for i := range linters {
		if linters[i].Command == fakeESLint {
			found = &linters[i]
		}
	}
	if found == nil {
		t.Fatalf("effectiveWorkspaceLinters() = %+v, want an entry for the workspace-root eslint at %q", linters, fakeESLint)
	}
}

// TestRunOnEditSkipsFileNotOnDisk is a regression test for spurious
// diagnostics on a buffer that has not been saved yet.
//
// Stdin linters get the buffer's content directly, so it is tempting to assume
// the file's absence from disk does not matter. It does: a type-aware linter
// builds its view of the project from disk and rejects a path it cannot find
// there. Editing a new file through the MCP tools (which write the buffer, not
// the file) produced eslint's
//
//	Parsing error: "parserOptions.project" has been provided for
//	@typescript-eslint/parser. The file was not found in any of the
//	provided project(s)
//
// on every keystroke until the first save — noise about the file's absence
// rather than anything about its contents.
func TestRunOnEditSkipsFileNotOnDisk(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Linters: []config.LinterConfig{{
		Extensions: []string{"txt"},
		// `true` exits 0 and prints nothing, so a run that happens is
		// detectable only by the manager's own bookkeeping below.
		Command: "true",
		Args:    []string{"{file}"},
		Format:  "eslint-json",
		Stdin:   true,
	}}}
	m := NewManager(cfg, dir)

	absent := filepath.Join(dir, "not-saved-yet.txt")
	m.RunOnEdit(absent, "some buffer content")
	if m.startedRun(absent) {
		t.Error("linted a path with no file on disk; a type-aware linter reports that " +
			"absence as a parse error on the user's unsaved buffer")
	}

	// The same path becomes lintable the moment it exists.
	present := filepath.Join(dir, "saved.txt")
	if err := os.WriteFile(present, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.RunOnEdit(present, "some buffer content")
	if !m.startedRun(present) {
		t.Error("skipped a file that does exist on disk; live linting is now dead")
	}
}

// startedRun reports whether runAsync claimed path, i.e. a lint actually
// started for it. Reading the manager's own map avoids racing the run's
// completion, which would make the assertion timing-dependent.
func (m *Manager) startedRun(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, seen := m.content[path]
	return seen
}

// TestPerFileLinterRunsInItsOwnPackage is the regression test for a mismatch
// between two decisions that both answer "which package does this file belong
// to?": findLinter resolved the binary by walking up from the file (so a
// package's own non-hoisted node_modules wins), while runLinter always set the
// cwd to the workspace root.
//
// Tools act on the cwd, not on where their binary lives. ESLint 9 discovers
// flat config from the cwd upward rather than from the linted file, and a
// relative `parserOptions.project` resolves against the cwd unless
// tsconfigRootDir says otherwise — so a package-local eslint would run under
// the root's config and the root's tsconfig, and report errors about the wrong
// project.
func TestPerFileLinterRunsInItsOwnPackage(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "services", "harmony")
	binDir := filepath.Join(pkg, "node_modules", ".bin")
	appDir := filepath.Join(pkg, "app")
	for _, d := range []string{binDir, appDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A stand-in "eslint" that records the cwd it was run in and emits a valid
	// empty eslint-json report.
	marker := filepath.Join(root, "cwd.txt")
	script := fmt.Sprintf("#!/bin/sh\npwd > %s\necho '[]'\n", marker)
	if err := os.WriteFile(filepath.Join(binDir, "eslint"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(appDir, "thing.ts")
	if err := os.WriteFile(file, []byte("export const x = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(&config.Config{}, root)
	lc, ok := m.findLinter(file, "ts")
	if !ok {
		t.Fatal("findLinter did not resolve the package-local eslint")
	}
	if _, err := runLinter(lc, file, "export const x = 1;\n", root); err != nil {
		t.Fatalf("runLinter: %v", err)
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the linter did not run: %v", err)
	}
	// Compare resolved forms on both sides: the shell may report either the
	// logical or the physical path, and TMPDIR is itself symlinked on macOS.
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	if cwd, want := resolve(strings.TrimSpace(string(got))), resolve(pkg); cwd != want {
		t.Errorf("linter ran in %q, want its own package %q — it would discover the "+
			"workspace root's config instead of the package's", cwd, want)
	}
}
