package server

import (
	"context"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

func newWorkspaceDiagTestService(buffers map[uint32]*document.Buffer) *editorService {
	entries := make(map[uint32]*bufferEntry, len(buffers))
	for id, buf := range buffers {
		entries[id] = &bufferEntry{buf: buf}
	}
	return &editorService{
		cfg:     &config.Config{},
		buffers: entries,
		lspMgr:  lsp.NewManager(".", nil),
		lintMgr: &lint.Manager{},
	}
}

// TestGetWorkspaceDiagnosticsAggregatesAcrossBuffers is a regression test:
// unlike GetDiagnostics (scoped to one bufID), GetWorkspaceDiagnostics must
// cover every open buffer and tag each diagnostic with its own file's path.
func TestGetWorkspaceDiagnosticsAggregatesAcrossBuffers(t *testing.T) {
	bufA := document.New("a.go", "hello\n")
	bufB := document.New("b.go", "world\n")
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{1: bufA, 2: bufB})

	if err := s.PluginPublishDiagnostics(1, "myplugin", bufA.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue in a")}); err != nil {
		t.Fatalf("publish for buffer 1 errored: %v", err)
	}
	if err := s.PluginPublishDiagnostics(2, "myplugin", bufB.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue in b")}); err != nil {
		t.Fatalf("publish for buffer 2 errored: %v", err)
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnostics(context.Background(), func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnostics errored: %v", err)
	}
	if res.Truncated() {
		t.Error("Truncated() = true, want false")
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != 2 {
		t.Fatalf("Items().Len() = %d, want 2", items.Len())
	}
	seen := map[string]string{}
	for i := range items.Len() {
		item := items.At(i)
		path, _ := item.Path()
		msg, _ := item.Message_()
		seen[path] = msg
	}
	if seen["a.go"] != "issue in a" {
		t.Errorf("a.go diagnostic = %q, want %q", seen["a.go"], "issue in a")
	}
	if seen["b.go"] != "issue in b" {
		t.Errorf("b.go diagnostic = %q, want %q", seen["b.go"], "issue in b")
	}
}

// TestGetWorkspaceDiagnosticsExcludesStaleEntries verifies the same
// version-staleness filtering GetDiagnostics applies also applies here —
// mergedDiagnostics/currentPluginDiags are shared between the two.
func TestGetWorkspaceDiagnosticsExcludesStaleEntries(t *testing.T) {
	buf := document.New("a.go", "hello\n")
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{1: buf})

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue")}); err != nil {
		t.Fatalf("publish errored: %v", err)
	}
	buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})

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
	if items.Len() != 0 {
		t.Errorf("Items().Len() = %d, want 0 (diagnostic computed against a stale version)", items.Len())
	}
}

// TestGetWorkspaceDiagnosticsTruncates verifies the maxWorkspaceDiagnostics
// cap actually caps the result and reports truncated=true, rather than
// producing an unbounded payload.
func TestGetWorkspaceDiagnosticsTruncates(t *testing.T) {
	buf := document.New("a.go", "hello\n")
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{1: buf})

	var diags []plugin.PluginDiagnostic
	for i := 0; i < maxWorkspaceDiagnostics+10; i++ {
		diags = append(diags, testPluginDiag(0, 0, "issue"))
	}
	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), diags); err != nil {
		t.Fatalf("publish errored: %v", err)
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnostics(context.Background(), func(proto.EditorService_getWorkspaceDiagnostics_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnostics errored: %v", err)
	}
	if !res.Truncated() {
		t.Error("Truncated() = false, want true")
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != maxWorkspaceDiagnostics {
		t.Errorf("Items().Len() = %d, want %d", items.Len(), maxWorkspaceDiagnostics)
	}
}

// TestGetWorkspaceDiagnosticsSummaryCounts verifies the counts-only RPC
// tallies severities and distinct-files-with-issues correctly across
// multiple buffers.
func TestGetWorkspaceDiagnosticsSummaryCounts(t *testing.T) {
	bufA := document.New("a.go", "hello\n")
	bufB := document.New("b.go", "world\n")
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{1: bufA, 2: bufB})

	errDiag := plugin.PluginDiagnostic{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 1, Severity: plugin.DiagnosticSeverityError, Message: "err"}
	warnDiag := plugin.PluginDiagnostic{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 1, Severity: plugin.DiagnosticSeverityWarning, Message: "warn"}
	infoDiag := plugin.PluginDiagnostic{FromLine: 0, FromCol: 0, ToLine: 0, ToCol: 1, Severity: plugin.DiagnosticSeverityInfo, Message: "info"}

	if err := s.PluginPublishDiagnostics(1, "myplugin", bufA.Version(), []plugin.PluginDiagnostic{errDiag, warnDiag}); err != nil {
		t.Fatalf("publish for buffer 1 errored: %v", err)
	}
	if err := s.PluginPublishDiagnostics(2, "myplugin", bufB.Version(), []plugin.PluginDiagnostic{infoDiag}); err != nil {
		t.Fatalf("publish for buffer 2 errored: %v", err)
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnosticsSummary(context.Background(), func(proto.EditorService_getWorkspaceDiagnosticsSummary_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnosticsSummary errored: %v", err)
	}
	if res.ErrorCount() != 1 {
		t.Errorf("ErrorCount() = %d, want 1", res.ErrorCount())
	}
	if res.WarningCount() != 1 {
		t.Errorf("WarningCount() = %d, want 1", res.WarningCount())
	}
	if res.InfoCount() != 1 {
		t.Errorf("InfoCount() = %d, want 1", res.InfoCount())
	}
	if res.FileCount() != 2 {
		t.Errorf("FileCount() = %d, want 2", res.FileCount())
	}
}

// TestGetWorkspaceDiagnosticsSummaryEmptyWorkspace verifies an empty/clean
// workspace reports all zero counts rather than erroring.
func TestGetWorkspaceDiagnosticsSummaryEmptyWorkspace(t *testing.T) {
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{
		1: document.New("a.go", "hello\n"),
	})

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetWorkspaceDiagnosticsSummary(context.Background(), func(proto.EditorService_getWorkspaceDiagnosticsSummary_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetWorkspaceDiagnosticsSummary errored: %v", err)
	}
	if res.ErrorCount() != 0 || res.WarningCount() != 0 || res.InfoCount() != 0 || res.FileCount() != 0 {
		t.Errorf("counts = (%d,%d,%d,%d), want all zero", res.ErrorCount(), res.WarningCount(), res.InfoCount(), res.FileCount())
	}
}
