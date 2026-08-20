package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	proto "github.com/indiejames/indigo/internal/proto"
)

// waitForScan polls until mgr's async workspace scan has finished, failing
// the test after a generous timeout — mirrors internal/lint's own
// waitForWorkspaceScan helper, duplicated here since that one is
// package-private to internal/lint.
func waitForScan(t *testing.T, mgr *lint.Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !mgr.WorkspaceScanning() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("workspace scan did not finish in time")
}

// newScannedWorkspaceService builds an editorService whose lintMgr has
// already completed a real (subprocess-backed, via `sh -c echo`) workspace
// scan finding one issue in <workDir>/scanned.go, plus whatever open
// buffers the caller supplies. Exercises the "unopened file" half of
// allWorkspaceDiagnostics (and, when a buffer at the same path is also
// supplied, the open-buffer-supersedes-scan precedence).
func newScannedWorkspaceService(t *testing.T, buffers map[uint32]*document.Buffer) (svc *editorService, workDir, scannedPath string) {
	t.Helper()
	workDir = t.TempDir()
	scannedPath = filepath.Join(workDir, "scanned.go")
	lc := config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "sh",
		WorkspaceArgs: []string{"-c",
			`echo '{"Issues":[{"FromLinter":"x","Text":"scanned issue","Severity":"warning","Pos":{"Filename":"scanned.go","Line":1,"Column":1}}]}'`},
		Format: "golangci-lint-json",
	}
	lintMgr := lint.NewManager(&config.Config{Linters: []config.LinterConfig{lc}}, workDir)
	lintMgr.ScanWorkspace()
	waitForScan(t, lintMgr)

	entries := make(map[uint32]*bufferEntry, len(buffers))
	for id, buf := range buffers {
		entries[id] = &bufferEntry{buf: buf}
	}
	svc = &editorService{
		cfg:     &config.Config{},
		buffers: entries,
		lspMgr:  lsp.NewManager(".", nil),
		lintMgr: lintMgr,
	}
	return svc, workDir, scannedPath
}

// TestGetWorkspaceDiagnosticsIncludesScannedUnopenedFiles is a regression
// test for the phase-2 lint workspace scan: a file nobody has open should
// still show up in GetWorkspaceDiagnostics/Summary once a workspace scan
// has found something in it.
func TestGetWorkspaceDiagnosticsIncludesScannedUnopenedFiles(t *testing.T) {
	s, _, scannedPath := newScannedWorkspaceService(t, nil)

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnostics(context.Background(), func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnostics errored: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != 1 {
		t.Fatalf("Items().Len() = %d, want 1", items.Len())
	}
	path, _ := items.At(0).Path()
	if path != scannedPath {
		t.Errorf("Path() = %q, want %q", path, scannedPath)
	}
	msg, _ := items.At(0).Message_()
	if msg != "scanned issue" {
		t.Errorf("Message() = %q, want %q", msg, "scanned issue")
	}

	sumFut, sumRel := client.GetWorkspaceDiagnosticsSummary(context.Background(), func(proto.EditorService_getWorkspaceDiagnosticsSummary_Params) error {
		return nil
	})
	defer sumRel()
	sumRes, err := sumFut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnosticsSummary errored: %v", err)
	}
	if sumRes.WarningCount() != 1 {
		t.Errorf("WarningCount() = %d, want 1", sumRes.WarningCount())
	}
	if sumRes.FileCount() != 1 {
		t.Errorf("FileCount() = %d, want 1", sumRes.FileCount())
	}
}

// TestGetWorkspaceDiagnosticsOpenBufferSupersedesScanResult verifies that
// when a file has both an open buffer and a (possibly stale) workspace scan
// result, only the open buffer's live diagnostics are reported — never both
// — since the scan result for that path could be arbitrarily out of date.
func TestGetWorkspaceDiagnosticsOpenBufferSupersedesScanResult(t *testing.T) {
	// buffers is populated in a second step below, once scannedPath is known
	// from the scan's own workDir/scanned.go naming.
	s, _, scannedPath := newScannedWorkspaceService(t, nil)
	s.buffers[1] = &bufferEntry{buf: document.New(scannedPath, "package main\n")}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnostics(context.Background(), func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnostics errored: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	// scannedPath has both an open buffer (with no diagnostics of its own)
	// and a stale scan entry — the scan entry must not leak through.
	if items.Len() != 0 {
		t.Fatalf("Items().Len() = %d, want 0 (open buffer must supersede the scan result)", items.Len())
	}
}

// TestRescanWorkspaceDiagnosticsTriggersScan is a regression test for the
// rescanWorkspaceDiagnostics RPC: it must actually kick off
// lintMgr.ScanWorkspace, not just accept the call and do nothing.
func TestRescanWorkspaceDiagnosticsTriggersScan(t *testing.T) {
	workDir := t.TempDir()
	lc := config.LinterConfig{
		Extensions: []string{"go"},
		Command:    "sh",
		WorkspaceArgs: []string{"-c",
			`echo '{"Issues":[{"FromLinter":"x","Text":"rescanned","Severity":"error","Pos":{"Filename":"r.go","Line":1,"Column":1}}]}'`},
		Format: "golangci-lint-json",
	}
	lintMgr := lint.NewManager(&config.Config{Linters: []config.LinterConfig{lc}}, workDir)
	s := &editorService{
		cfg:     &config.Config{},
		buffers: map[uint32]*bufferEntry{},
		lspMgr:  lsp.NewManager(".", nil),
		lintMgr: lintMgr,
	}

	if snap := lintMgr.WorkspaceScanSnapshot(); len(snap) != 0 {
		t.Fatalf("expected no scan results before any scan, got %+v", snap)
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.RescanWorkspaceDiagnostics(context.Background(), func(proto.EditorService_rescanWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("RescanWorkspaceDiagnostics errored: %v", err)
	}

	waitForScan(t, lintMgr)

	snap := lintMgr.WorkspaceScanSnapshot()
	if len(snap) != 1 {
		t.Fatalf("len(snap) = %d, want 1 after rescan: %+v", len(snap), snap)
	}
}
