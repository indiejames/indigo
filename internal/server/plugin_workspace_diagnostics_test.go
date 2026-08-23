package server

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/plugin"
)

func TestPluginPublishWorkspaceDiagnosticsStoresAndMerges(t *testing.T) {
	s := newWorkspaceDiagTestService(nil)

	if err := s.PluginPublishWorkspaceDiagnostics("myplugin", "/repo/notes.md", []plugin.PluginDiagnostic{testPluginDiag(1, 0, "typo")}); err != nil {
		t.Fatalf("PluginPublishWorkspaceDiagnostics errored: %v", err)
	}

	items := s.allWorkspaceDiagnostics()
	if len(items) != 1 || items[0].path != "/repo/notes.md" || items[0].d.Message != "typo" || items[0].d.Source != "myplugin" {
		t.Errorf("allWorkspaceDiagnostics = %+v, want one item for /repo/notes.md from myplugin", items)
	}
}

// TestPluginPublishWorkspaceDiagnosticsMultiplePluginsSamePath verifies two
// plugins publishing for the same unopened file both show up, mirroring how
// LSP+lint+plugin diagnostics all merge (non-exclusively) for an open buffer.
func TestPluginPublishWorkspaceDiagnosticsMultiplePluginsSamePath(t *testing.T) {
	s := newWorkspaceDiagTestService(nil)

	if err := s.PluginPublishWorkspaceDiagnostics("spell", "/repo/readme.md", []plugin.PluginDiagnostic{testPluginDiag(0, 0, "misspelled")}); err != nil {
		t.Fatalf("publish spell: %v", err)
	}
	if err := s.PluginPublishWorkspaceDiagnostics("other", "/repo/readme.md", []plugin.PluginDiagnostic{testPluginDiag(1, 0, "other issue")}); err != nil {
		t.Fatalf("publish other: %v", err)
	}

	items := s.allWorkspaceDiagnostics()
	if len(items) != 2 {
		t.Fatalf("allWorkspaceDiagnostics returned %d items, want 2 (one per plugin)", len(items))
	}
}

// TestPluginPublishWorkspaceDiagnosticsEmptyClears mirrors
// TestPluginPublishDiagnosticsEmptyClears: republishing an empty list for
// (plugin, path) must remove the previously stored entry entirely, not
// leave a stale empty slice behind.
func TestPluginPublishWorkspaceDiagnosticsEmptyClears(t *testing.T) {
	s := newWorkspaceDiagTestService(nil)

	if err := s.PluginPublishWorkspaceDiagnostics("myplugin", "/repo/notes.md", []plugin.PluginDiagnostic{testPluginDiag(0, 0, "issue")}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := s.PluginPublishWorkspaceDiagnostics("myplugin", "/repo/notes.md", nil); err != nil {
		t.Fatalf("publish empty: %v", err)
	}

	s.mu.Lock()
	_, pathStillTracked := s.pluginWorkspaceDiags["/repo/notes.md"]
	s.mu.Unlock()
	if pathStillTracked {
		t.Error("path entry still present in pluginWorkspaceDiags after publishing an empty diagnostics list")
	}
	if items := s.allWorkspaceDiagnostics(); len(items) != 0 {
		t.Errorf("allWorkspaceDiagnostics = %+v, want empty after clearing", items)
	}
}

// TestPluginPublishWorkspaceDiagnosticsOpenBufferSupersedes is a regression
// test for allWorkspaceDiagnostics' documented merge rule: a path that is
// currently open in a buffer must not also surface its scanned/published
// workspace-diagnostic entry — only the live per-buffer diagnostics count,
// since a scan result can be arbitrarily stale relative to live edits.
func TestPluginPublishWorkspaceDiagnosticsOpenBufferSupersedes(t *testing.T) {
	buf := document.New("/repo/open.go", "package main\n")
	s := newWorkspaceDiagTestService(map[uint32]*document.Buffer{1: buf})

	if err := s.PluginPublishWorkspaceDiagnostics("myplugin", "/repo/open.go", []plugin.PluginDiagnostic{testPluginDiag(0, 0, "stale scan result")}); err != nil {
		t.Fatalf("PluginPublishWorkspaceDiagnostics errored: %v", err)
	}

	items := s.allWorkspaceDiagnostics()
	for _, it := range items {
		if it.d.Message == "stale scan result" {
			t.Errorf("allWorkspaceDiagnostics included a scan-published diagnostic for an open buffer's path: %+v", it)
		}
	}
}
