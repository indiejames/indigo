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
