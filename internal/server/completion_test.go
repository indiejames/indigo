package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// buildAndInstallMiniPlugin builds internal/plugin's miniplugin test fixture
// (which registers both a key binding and a completion provider — see
// internal/plugin/testdata/miniplugin/main.go) and installs it into a fake
// `indigo plugins` directory under a temp XDG_CONFIG_HOME, exactly like
// internal/plugin's own TestManagerCompletionProviderRealPlugin. Reused here
// (rather than a second fixture) to prove Complete/ResolveCompletion merge a
// real plugin-sourced item end-to-end through the capnp RPC boundary, not
// just that plugin.Manager's own fan-out works.
func buildAndInstallMiniPlugin(t *testing.T) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	pluginDir := filepath.Join(configHome, "indigo", "plugins", "miniplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(pluginDir, "miniplugin")
	cmd := exec.Command("go", "build", "-o", out, "../plugin/testdata/miniplugin")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build miniplugin: %v\n%s", err, output)
	}

	manifest := "name = \"miniplugin\"\nversion = \"0.0.1\"\n\n[binaries]\n\"" +
		runtime.GOOS + "/" + runtime.GOARCH + "\" = \"miniplugin\"\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCompleteMergesPluginAndLSPItems is a regression/coverage test for the
// editorService.Complete change that added a pluginMgr.GetCompletions call
// alongside the existing lspMgr.Complete call: with no language server
// attached to the test buffer's path, lspMgr.Complete contributes nothing,
// so a successful non-empty result here proves the plugin item actually flows
// through Complete's response, tagged with Source = the plugin's name.
func TestCompleteMergesPluginAndLSPItems(t *testing.T) {
	buildAndInstallMiniPlugin(t)

	dir := t.TempDir()
	pluginMgr := plugin.NewManager(dir, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := pluginMgr.Start(ctx); err != nil {
		t.Fatalf("plugin manager Start: %v", err)
	}
	t.Cleanup(pluginMgr.Shutdown)

	path := filepath.Join(dir, "main.go")
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "package main\n"), canonPath: canonicalPath(path)},
		},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   pluginMgr,
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.Complete(context.Background(), func(p proto.EditorService_complete_Params) error {
		p.SetBufId(1)
		p.SetLine(0)
		p.SetCol(0)
		return nil
	})
	res, err := fut.Struct()
	if err != nil {
		rel()
		t.Fatalf("Complete: %v", err)
	}
	items, err := res.Items()
	if err != nil {
		rel()
		t.Fatalf("Items: %v", err)
	}
	if items.Len() != 1 {
		rel()
		t.Fatalf("Complete returned %d items, want 1 (the plugin's)", items.Len())
	}
	item := items.At(0)
	label, _ := item.Label()
	if label != "mini-item" {
		t.Errorf("item.Label = %q, want %q", label, "mini-item")
	}
	source, _ := item.Source()
	if source != "miniplugin" {
		t.Errorf("item.Source = %q, want %q", source, "miniplugin")
	}

	// ResolveCompletion must route on Source to the plugin manager, not the
	// (in this test, LSP-client-less) language server manager. item must be
	// read from before rel() releases the Complete response message, so the
	// ResolveCompletion round trip happens first, still under that defer.
	rfut, rrel := client.ResolveCompletion(context.Background(), func(p proto.EditorService_resolveCompletion_Params) error {
		p.SetBufId(1)
		return p.SetItem(item)
	})
	rres, err := rfut.Struct()
	if err != nil {
		rrel()
		rel()
		t.Fatalf("ResolveCompletion: %v", err)
	}
	resolvedItem, err := rres.Item()
	if err != nil {
		rrel()
		rel()
		t.Fatalf("resolved Item: %v", err)
	}
	detail, _ := resolvedItem.Detail()
	if detail != "resolved:resolve-me" {
		t.Errorf("resolved.Detail = %q, want %q", detail, "resolved:resolve-me")
	}
	rrel()
	rel()
}
