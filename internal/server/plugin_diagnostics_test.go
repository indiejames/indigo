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

func newDiagTestService(bufID uint32, buf *document.Buffer) *editorService {
	return &editorService{
		cfg:     &config.Config{},
		buffers: map[uint32]*bufferEntry{bufID: {buf: buf}},
		lspMgr:  lsp.NewManager(".", nil),
		lintMgr: &lint.Manager{},
	}
}

func testPluginDiag(line, col int, msg string) plugin.PluginDiagnostic {
	return plugin.PluginDiagnostic{
		FromLine: uint32(line), FromCol: uint32(col),
		ToLine: uint32(line), ToCol: uint32(col + 1),
		Severity: plugin.DiagnosticSeverityWarning,
		Message:  msg,
	}
}

func TestPluginPublishDiagnosticsStoresUnderCurrentVersion(t *testing.T) {
	buf := document.New("test.go", "hello\n")
	s := newDiagTestService(1, buf)

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue")}); err != nil {
		t.Fatalf("PluginPublishDiagnostics errored: %v", err)
	}

	s.mu.Lock()
	got := s.buffers[1].pluginDiags["myplugin"].diags
	s.mu.Unlock()
	if len(got) != 1 || got[0].Message != "issue" || got[0].Source != "myplugin" {
		t.Errorf("pluginDiags[myplugin] = %+v, want one diagnostic with Message=issue Source=myplugin", got)
	}
	if got[0].Severity != lsp.SeverityWarning {
		t.Errorf("Severity = %v, want SeverityWarning", got[0].Severity)
	}
}

// TestPluginPublishDiagnosticsRejectsStaleVersion is a regression test: a
// plugin that computed diagnostics against old content (version has since
// advanced via a live edit) must not be allowed to overwrite/clobber
// current diagnostics with stale ones.
func TestPluginPublishDiagnosticsRejectsStaleVersion(t *testing.T) {
	buf := document.New("test.go", "hello\n")
	s := newDiagTestService(1, buf)

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "fresh")}); err != nil {
		t.Fatalf("initial publish errored: %v", err)
	}

	staleVersion := buf.Version()
	buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
	if buf.Version() == staleVersion {
		t.Fatal("test setup: buffer version did not advance after Apply")
	}

	if err := s.PluginPublishDiagnostics(1, "myplugin", staleVersion, []plugin.PluginDiagnostic{testPluginDiag(0, 0, "stale")}); err != nil {
		t.Fatalf("PluginPublishDiagnostics errored: %v", err)
	}

	s.mu.Lock()
	got := s.buffers[1].pluginDiags["myplugin"].diags
	s.mu.Unlock()
	if len(got) != 1 || got[0].Message != "fresh" {
		t.Errorf("pluginDiags[myplugin] = %+v, want the original 'fresh' diagnostic unchanged (stale publish must be discarded)", got)
	}
}

// TestGetDiagnosticsExcludesDiagnosticsInvalidatedByLaterEdit is a
// regression test: the publish-time staleness check alone only stops an old
// publish from overwriting a newer one — it does nothing once accepted
// diagnostics are left behind by edits that land *after* the publish, with
// no guarantee the plugin will ever republish (e.g. it crashed, or only
// recomputes on save). Left unchecked, GetDiagnostics would keep returning
// diagnostics pointing at text that no longer matches their line/col range.
func TestGetDiagnosticsExcludesDiagnosticsInvalidatedByLaterEdit(t *testing.T) {
	buf := document.New("test.go", "hello\n")
	s := newDiagTestService(1, buf)

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue")}); err != nil {
		t.Fatalf("PluginPublishDiagnostics errored: %v", err)
	}

	// The buffer moves on without the plugin ever republishing.
	buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetDiagnostics(context.Background(), func(p proto.EditorService_getDiagnostics_Params) error {
		p.SetBufId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetDiagnostics errored: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != 0 {
		t.Errorf("Items().Len() = %d, want 0 — the diagnostic was computed against an older version and should be excluded, not shown pointing at stale text", items.Len())
	}
}

func TestPluginPublishDiagnosticsEmptyClears(t *testing.T) {
	buf := document.New("test.go", "hello\n")
	s := newDiagTestService(1, buf)

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue")}); err != nil {
		t.Fatalf("initial publish errored: %v", err)
	}
	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), nil); err != nil {
		t.Fatalf("clearing publish errored: %v", err)
	}

	s.mu.Lock()
	_, stillPresent := s.buffers[1].pluginDiags["myplugin"]
	s.mu.Unlock()
	if stillPresent {
		t.Error("pluginDiags[myplugin] still present after publishing an empty diagnostics list")
	}
}

func TestPluginPublishDiagnosticsUnknownBufferErrors(t *testing.T) {
	s := newDiagTestService(1, document.New("test.go", "hello\n"))
	if err := s.PluginPublishDiagnostics(999, "myplugin", 0, nil); err == nil {
		t.Error("expected an error for an unknown bufID, got nil")
	}
}

// TestGetDiagnosticsMergesPluginDiagnostics is a regression test: plugin
// diagnostics published via PublishDiagnostics must show up in the same
// GetDiagnostics response LSP/lint diagnostics populate (status bar counts,
// diagnostics popup), not sit in a separate, invisible cache.
func TestGetDiagnosticsMergesPluginDiagnostics(t *testing.T) {
	buf := document.New("test.go", "hello\n")
	s := newDiagTestService(1, buf)

	if err := s.PluginPublishDiagnostics(1, "myplugin", buf.Version(), []plugin.PluginDiagnostic{testPluginDiag(0, 0, "spelling issue")}); err != nil {
		t.Fatalf("PluginPublishDiagnostics errored: %v", err)
	}

	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	fut, rel := client.GetDiagnostics(context.Background(), func(p proto.EditorService_getDiagnostics_Params) error {
		p.SetBufId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("GetDiagnostics errored: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		t.Fatalf("Items() errored: %v", err)
	}
	if items.Len() != 1 {
		t.Fatalf("Items().Len() = %d, want 1", items.Len())
	}
	item := items.At(0)
	msg, _ := item.Message_()
	src, _ := item.Source()
	if msg != "spelling issue" || src != "myplugin" {
		t.Errorf("item = {message: %q, source: %q}, want {message: \"spelling issue\", source: \"myplugin\"}", msg, src)
	}
}
