package plugin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// buildMiniPlugin compiles testdata/miniplugin into dir/miniplugin and
// returns the binary's absolute path. Building a real plugin binary (rather
// than hand-rolling a fake RPC server) is what lets this test exercise
// Manager.Start's actual spawn → socket handshake → Initialize → dispatch
// path end-to-end, the gap flagged in the test-coverage backlog: "no test
// spawns a real plugin process."
func buildMiniPlugin(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "miniplugin")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/miniplugin")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build miniplugin: %v\n%s", err, output)
	}
	return out
}

// setUpMiniPluginInstall creates a fake `indigo plugins` install directory
// under a temp XDG_CONFIG_HOME containing miniplugin's manifest and binary,
// and points the manager's plugin discovery at it via t.Setenv.
func setUpMiniPluginInstall(t *testing.T) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	pluginDir := filepath.Join(configHome, "indigo", "plugins", "miniplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	binPath := buildMiniPlugin(t, pluginDir)
	binName := filepath.Base(binPath)

	manifest := "name = \"miniplugin\"\nversion = \"0.0.1\"\n\n[binaries]\n\"" +
		runtime.GOOS + "/" + runtime.GOARCH + "\" = \"" + binName + "\"\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestManagerStartSpawnsRealPluginAndDispatchesKey is an end-to-end
// regression/coverage test: it spawns a real plugin process (not a fake),
// verifies Manager.Start's socket handshake and Initialize RPC actually
// register the plugin, then drives a genuine HandleKey RPC round-trip
// through the spawned process and checks the returned edit.
func TestManagerStartSpawnsRealPluginAndDispatchesKey(t *testing.T) {
	setUpMiniPluginInstall(t)

	m := NewManager(t.TempDir(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Ensure the spawned process is stopped even if an assertion below
	// fails partway through (t.Fatal would otherwise skip teardown).
	t.Cleanup(m.Shutdown)

	m.mu.Lock()
	n := len(m.plugins)
	var reg *registeredPlugin
	if n > 0 {
		reg = m.plugins[0]
	}
	m.mu.Unlock()
	if n != 1 || reg == nil {
		t.Fatalf("m.plugins has %d entries, want exactly 1 (miniplugin registered)", n)
	}
	if reg.name != "miniplugin" {
		t.Errorf("registered plugin name = %q, want %q", reg.name, "miniplugin")
	}
	if reg.process == nil {
		t.Fatal("registered plugin has no process handle")
	}

	// Drive a real HandleKey RPC round-trip: miniplugin registered "X" via
	// api.OnKey in its actual (separate-process) Init, so a successful
	// handled=true + matching edit here proves the whole spawn/handshake/
	// dispatch chain works, not just that a process started.
	handled, edits, _, _, _, _, err := m.HandleKey(context.Background(), "X", "normal", 1, 1, 0, 0)
	if err != nil {
		t.Fatalf("HandleKey: %v", err)
	}
	if !handled {
		t.Fatal("HandleKey: handled = false, want true (miniplugin's \"X\" binding should have fired)")
	}
	if len(edits) != 1 || edits[0].NewText != "miniplugin-was-here" {
		t.Errorf("edits = %+v, want a single edit with NewText %q", edits, "miniplugin-was-here")
	}

	// Simulate a crash and verify the process actually gets reaped —
	// reapOnDisconnect is wired into the real Start path this test just
	// exercised above, so a genuine process crash should trigger it.
	// Synchronize on reg.reapDone (closed once reapOnDisconnect's own
	// Wait() completes) rather than polling the pid, since polling via
	// Signal(0) can't portably distinguish "still alive" from "exited but
	// not yet reaped" the way the completion channel does directly.
	if err := reg.process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case <-reg.reapDone:
	case <-time.After(5 * time.Second):
		t.Error("plugin process was not reaped within the timeout after crashing")
	}
}
