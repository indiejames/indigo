package plugin

import (
	"context"
	"testing"
	"time"
)

// TestPluginStartingAfterShutdownIsNotPublished is the regression for a plugin
// whose startup finished after Shutdown had taken its snapshot of m.plugins:
// startPlugin appended it anyway, so it was listed on a manager that had
// already shut down and its process was never closed or killed. (It also set
// rpcConn after publishing, which Shutdown read unsynchronized.)
func TestPluginStartingAfterShutdownIsNotPublished(t *testing.T) {
	setUpMiniPluginInstall(t)

	m := NewManager(t.TempDir(), nil)
	m.Shutdown() // before Start: every plugin now finishes starting "late"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m.Start(ctx) //nolint:errcheck // per-plugin failures are logged, not returned

	m.mu.Lock()
	n := len(m.plugins)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("m.plugins has %d entries after Shutdown, want 0 (plugin published and left running)", n)
	}
}
