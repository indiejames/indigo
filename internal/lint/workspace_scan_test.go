package lint

import (
	"context"
	"errors"
	"strings"
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

// Note: runWorkspaceScan's coalesced-handoff fix (workspaceRunning now
// stays true across a pending follow-up run instead of briefly dropping to
// false between the two, see its doc comment) has no dedicated regression
// test — the window it closes is a handful of instructions between an
// unlock and a re-lock, too narrow for any black-box poll loop to reliably
// observe either with or without the fix, so a timing-based test here would
// either be flaky or silently prove nothing (confirmed: a naive polling
// test passed identically against both the buggy and fixed code). Same
// class of "not reliably testable without test-only production hooks" call
// already made for a couple of other narrow races in this codebase (see
// CLAUDE.md's audit backlog, e.g. SaveAs/DiscardRecovery).

// TestShutdownCancelsInFlightWorkspaceScan is a regression test for
// Manager.Shutdown: without it, a long-running workspace-scan subprocess
// (up to workspaceScanTimeout, 2 minutes) would keep running orphaned after
// the server process that spawned it has already exited. Shutdown cancels
// scanCtx, which runWorkspaceLinter's context.WithTimeout derives from, so
// the scan should fail well before its own timeout once Shutdown is called.
func TestShutdownCancelsInFlightWorkspaceScan(t *testing.T) {
	lc := config.LinterConfig{
		Extensions:    []string{"go"},
		Command:       "sh",
		WorkspaceArgs: []string{"-c", "sleep 30 && echo '{\"Issues\":[]}'"},
		Format:        "golangci-lint-json",
	}
	m := newWorkspaceTestManager(t.TempDir(), lc)

	m.ScanWorkspace()
	time.Sleep(20 * time.Millisecond) // let the subprocess actually start
	m.Shutdown()

	waitForWorkspaceScan(t, m) // should resolve almost immediately, not after 30s

	if err := m.WorkspaceScanError("sh"); err == nil {
		t.Error("WorkspaceScanError = nil, want the cancellation error surfaced after Shutdown")
	}
}

// TestScanWorkspaceTimesOutAndKeepsStaleResults verifies runWorkspaceLinter
// actually enforces workspaceScanTimeout: a linter that hangs past it should
// be killed and its failure recorded as a deadline-exceeded error, with the
// previous (successful) scan's results left visible — same "leave stale
// data visible" contract TestScanWorkspaceKeepsStaleResultsOnFailure already
// covers for an outright command failure, exercised here for a timeout
// instead.
func TestScanWorkspaceTimesOutAndKeepsStaleResults(t *testing.T) {
	orig := workspaceScanTimeout
	workspaceScanTimeout = 100 * time.Millisecond
	t.Cleanup(func() { workspaceScanTimeout = orig })

	workDir := t.TempDir()
	lc := shWorkspaceJSON("sh", `{"Issues":[{"FromLinter":"x","Text":"boom","Severity":"error","Pos":{"Filename":"a.go","Line":1,"Column":1}}]}`)
	m := newWorkspaceTestManager(workDir, lc)

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)
	if len(m.WorkspaceScanSnapshot()) != 1 {
		t.Fatalf("expected 1 file populated by the first (successful) scan")
	}

	hanging := lc
	hanging.WorkspaceArgs = []string{"-c", "sleep 30"}
	m.userLints = []config.LinterConfig{hanging}

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)

	if len(m.WorkspaceScanSnapshot()) != 1 {
		t.Errorf("a timed-out rescan should leave the previous scan's results visible, got %+v", m.WorkspaceScanSnapshot())
	}
	err := m.WorkspaceScanError("sh")
	if err == nil {
		t.Fatal("WorkspaceScanError = nil, want a deadline-exceeded error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WorkspaceScanError = %v, want context.DeadlineExceeded", err)
	}
}

// TestScanWorkspaceFailureIncludesStderrPreview verifies a workspace-scan
// command that fails outright (non-zero exit, unparseable stdout) folds a
// preview of its stderr into WorkspaceScanError, not just a bare exit
// status — the only way that failure is visible at all, since there's no
// per-file editor context to hint at the cause the way an open buffer's own
// diagnostics would.
func TestScanWorkspaceFailureIncludesStderrPreview(t *testing.T) {
	lc := config.LinterConfig{
		Extensions:    []string{"go"},
		Command:       "sh",
		WorkspaceArgs: []string{"-c", "echo 'config error: bad rule xyz' >&2; exit 1"},
		Format:        "golangci-lint-json",
	}
	m := newWorkspaceTestManager(t.TempDir(), lc)

	m.ScanWorkspace()
	waitForWorkspaceScan(t, m)

	err := m.WorkspaceScanError("sh")
	if err == nil {
		t.Fatal("WorkspaceScanError = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "config error: bad rule xyz") {
		t.Errorf("WorkspaceScanError = %q, want it to include the command's stderr", err.Error())
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
