package lint

import (
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

	m.RunAsync("foo.go")

	if !m.pending["foo.go"] {
		t.Error("RunAsync while running should set pending, not launch a new run")
	}
	if diags := m.GetDiagnostics("foo.go"); diags != nil {
		t.Errorf("cache should be untouched by a coalesced call, got %v", diags)
	}
}

func TestManagerRunAsyncNoMatchingLinter(t *testing.T) {
	m := newTestManager() // no linters configured at all
	m.RunAsync("foo.go")  // must not panic or spawn anything

	if diags := m.GetDiagnostics("foo.go"); diags != nil {
		t.Errorf("diags = %v, want nil when no linter matches", diags)
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
