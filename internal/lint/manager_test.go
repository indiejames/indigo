package lint

import (
	"fmt"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lsp"
)

func newTestManager(lints ...config.LinterConfig) *Manager {
	return &Manager{
		userLints: lints,
		cached:    make(map[string][]lsp.Diagnostic),
		running:   make(map[string]bool),
		pending:   make(map[string]bool),
		content:   make(map[string]string),
		lastErr:   make(map[string]error),
	}
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

	m.run("foo.go", lc) // synchronous, bypassing the "go" in RunAsync

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

	m.run("foo.go", lc) // synchronous, bypassing the "go" in RunAsync

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
	m.running["foo.go"] = true

	m.RunOnEdit("foo.go", "latest buffer text")

	if !m.pending["foo.go"] {
		t.Error("RunOnEdit while running should set pending, not launch a new run")
	}
	if got := m.content["foo.go"]; got != "latest buffer text" {
		t.Errorf(`content["foo.go"] = %q, want %q`, got, "latest buffer text")
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
	m.run("foo.go", goodLC)
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
	m.run("foo.go", failingLC)

	if err := m.LastError("foo.go"); err == nil {
		t.Error("LastError = nil after a failing run, want the run's error")
	}
	if diags := m.GetDiagnostics("foo.go"); len(diags) != 1 {
		t.Errorf("GetDiagnostics after a failed run = %v, want the previous cache left untouched", diags)
	}

	// A subsequent successful run clears the error again.
	m.run("foo.go", goodLC)
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
}

func TestFindLinterUserOverridesAuto(t *testing.T) {
	userLC := config.LinterConfig{Extensions: []string{"go"}, Command: "user-linter", Format: "golangci-lint-json"}
	m := &Manager{
		userLints: []config.LinterConfig{userLC},
		autoLints: []config.LinterConfig{{Extensions: []string{"go"}, Command: "auto-linter", Format: "golangci-lint-json"}},
	}
	got, ok := m.findLinter("go")
	if !ok || got.Command != "user-linter" {
		t.Errorf("findLinter(go) = %+v, want user-linter to take precedence", got)
	}
}
