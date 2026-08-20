package lint

import (
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
)

// shWorkspaceJSON is a workspace-scan linter stand-in that needs no
// external tool installed: it shells out to `sh -c` and echoes a canned
// golangci-lint-shaped whole-project JSON blob (multiple files' worth of
// issues, unlike shJSON's single-file blob).
func shWorkspaceJSON(command, json string) config.LinterConfig {
	return config.LinterConfig{
		Extensions:    []string{"go"},
		Command:       command,
		WorkspaceArgs: []string{"-c", "echo '" + json + "'"},
		Format:        "golangci-lint-json",
	}
}

func newWorkspaceTestManager(workDir string, lints ...config.LinterConfig) *Manager {
	m := newTestManager()
	m.userLints = lints
	m.workDir = workDir
	return m
}

func TestScanWorkspacePopulatesSnapshotAcrossFiles(t *testing.T) {
	workDir := t.TempDir()
	lc := shWorkspaceJSON("sh", `{"Issues":[
		{"FromLinter":"x","Text":"boom a","Severity":"error","Pos":{"Filename":"a.go","Line":1,"Column":1}},
		{"FromLinter":"x","Text":"boom b","Severity":"warning","Pos":{"Filename":"sub/b.go","Line":2,"Column":3}}
	]}`)
	m := newWorkspaceTestManager(workDir, lc)

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)

	wantA := workDir + "/a.go"
	wantB := workDir + "/sub/b.go"
	snap := m.WorkspaceScanSnapshot()
	if len(snap) != 2 {
		t.Fatalf("len(snap) = %d, want 2: %+v", len(snap), snap)
	}
	aDiags := snap[wantA]
	if len(aDiags) != 1 || aDiags[0].Message != "boom a" {
		t.Errorf("a.go diags = %+v, want one 'boom a'", aDiags)
	}
	bDiags := snap[wantB]
	if len(bDiags) != 1 || bDiags[0].Message != "boom b" {
		t.Errorf("sub/b.go diags = %+v, want one 'boom b'", bDiags)
	}

	if got := m.WorkspaceScanDiagnostics(wantA); len(got) != 1 {
		t.Errorf("WorkspaceScanDiagnostics(a.go) = %+v, want 1 entry", got)
	}
}

func TestScanWorkspaceSkipsLintersWithoutWorkspaceArgs(t *testing.T) {
	lc := config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "sh",
		Args:       []string{"-c", "echo '{}'"}, // per-file only, no WorkspaceArgs
		Format:     "golangci-lint-json",
	}
	m := newWorkspaceTestManager(t.TempDir(), lc)

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)

	if snap := m.WorkspaceScanSnapshot(); len(snap) != 0 {
		t.Errorf("snap = %+v, want empty — linter has no WorkspaceArgs", snap)
	}
}

func TestScanWorkspaceCoalescesWhileRunning(t *testing.T) {
	m := newWorkspaceTestManager(t.TempDir())
	m.workspaceRunning = true

	m.ScanWorkspace()

	if !m.workspacePending {
		t.Error("ScanWorkspace while running should set workspacePending, not launch a new run")
	}
}

func TestScanWorkspaceKeepsStaleResultsOnFailure(t *testing.T) {
	lc := shWorkspaceJSON("sh", `{"Issues":[{"FromLinter":"x","Text":"boom","Severity":"error","Pos":{"Filename":"a.go","Line":1,"Column":1}}]}`)
	m := newWorkspaceTestManager(t.TempDir(), lc)

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)
	if len(m.WorkspaceScanSnapshot()) != 1 {
		t.Fatalf("expected 1 file populated by the first (successful) scan")
	}

	// Second scan with a linter command that fails outright.
	failing := lc
	failing.Command = "sh"
	failing.WorkspaceArgs = []string{"-c", "exit 1"}
	m.userLints = []config.LinterConfig{failing}

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)

	if len(m.WorkspaceScanSnapshot()) != 1 {
		t.Errorf("a failed rescan should leave the previous scan's results visible, got %+v", m.WorkspaceScanSnapshot())
	}
	if err := m.WorkspaceScanError("sh"); err == nil {
		t.Error("WorkspaceScanError should report the failure even though stale results are kept")
	}
}

func TestEffectiveWorkspaceLintersDedupesByCommandUserFirst(t *testing.T) {
	m := newTestManager()
	m.userLints = []config.LinterConfig{
		{Command: "golangci-lint", WorkspaceArgs: []string{"run", "./..."}, Format: "golangci-lint-json"},
	}
	m.autoLints = []config.LinterConfig{
		{Command: "golangci-lint", WorkspaceArgs: []string{"different-args"}, Format: "golangci-lint-json"},
		{Command: "eslint", WorkspaceArgs: []string{".", "--format", "json"}, Format: "eslint-json"},
		{Command: "ruff"}, // no WorkspaceArgs: skipped
	}

	got := m.effectiveWorkspaceLinters()
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].Command != "golangci-lint" || got[0].WorkspaceArgs[0] != "run" {
		t.Errorf("expected user config's golangci-lint entry to win, got %+v", got[0])
	}
}

// waitForWorkspaceScan polls until the manager's async scan (and any
// coalesced follow-up) has finished, or fails the test after a generous
// timeout — the scan itself runs a trivial `sh -c echo` subprocess, so this
// should resolve near-instantly outside of a genuinely broken case.
func waitForWorkspaceScan(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.workspaceMu.Lock()
		running := m.workspaceRunning
		m.workspaceMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("workspace scan did not finish in time")
}
