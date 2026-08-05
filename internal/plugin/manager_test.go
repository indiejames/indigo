package plugin

import (
	"context"
	"testing"
	"time"
)

// TestManagerWaitReady reproduces the bug where GetMenuItems/GetPluginBindings
// could race Manager.Start's background plugin startup and observe an empty
// (not-yet-populated) plugin list. WaitReady must not report ready before
// Start has finished, and must report ready promptly once it has.
func TestManagerWaitReady(t *testing.T) {
	m := NewManager(t.TempDir(), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	start := time.Now()
	m.WaitReady(ctx)
	cancel()
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("WaitReady returned after %v, before Start ran and before its own timeout elapsed", elapsed)
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() {
		m.WaitReady(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitReady did not return after Start completed")
	}
}

// TestInsertHookVisibilityDuringStartup verifies that insert hooks registered
// during plugin initialization are immediately visible to AllRegisteredInsertChars,
// even if the call interleaves with the plugin's Initialize call. This prevents
// a race where a client snapshot could miss hooks that were registered but not
// yet visible in m.plugins.
func TestInsertHookVisibilityDuringStartup(t *testing.T) {
	m := NewManager(t.TempDir(), nil)

	// Simulate a plugin being added to m.plugins with insert hooks already registered.
	// In the real flow, the plugin is now appended to m.plugins BEFORE Initialize,
	// so hooks registered during Initialize are immediately visible.
	reg := &registeredPlugin{
		name:        "test-plugin",
		insertHooks: make(map[string]pluginproto.KeyHandler),
	}

	m.mu.Lock()
	m.plugins = append(m.plugins, reg)
	m.mu.Unlock()

	// Simulate the plugin registering an insert hook during initialization.
	reg.mu.Lock()
	// We can't create a real KeyHandler without a Cap'n Proto connection,
	// but we can use an invalid one for the test since AllRegisteredInsertChars
	// only checks the map keys.
	reg.insertHooks[")"] = pluginproto.KeyHandler{}
	reg.mu.Unlock()

	// A concurrent client snapshot should now see the hook immediately.
	chars := m.AllRegisteredInsertChars()
	if len(chars) != 1 || chars[0] != ")" {
		t.Errorf("AllRegisteredInsertChars() = %v, want [\")\"]", chars)
	}
}
