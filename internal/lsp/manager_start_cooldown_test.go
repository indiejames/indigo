package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClientForPathDoesNotRetryFailedStartWithinCooldown is a regression
// test: clientForPath used to retry starting a language server on every
// call with no memory of recent failures. DidOpen/DidChange call
// clientForPath from a fire-and-forget goroutine on every keystroke (see
// server_buffer.go's ApplyOp), so a broken server (wrong toolchain, missing
// binary, crashes on startup, ...) got re-spawned on every single edit —
// each attempt spawning a real OS process and, pre-fix, blocking its own
// goroutine for up to Client.Initialize's 30s timeout. Continuous typing
// could pile up an unbounded number of these, creating enough process/
// goroutine pressure to make unrelated server RPCs miss their own
// deadlines. See failedStarts/startRetryCooldown in manager.go.
func TestClientForPathDoesNotRetryFailedStartWithinCooldown(t *testing.T) {
	dir := t.TempDir()
	counterPath := filepath.Join(dir, "counter")
	scriptPath := filepath.Join(dir, "fake-lsp.sh")
	script := "#!/bin/sh\necho x >> " + counterPath + "\nexit 1\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	servers := []ServerConfig{{Extensions: []string{"zzz"}, Command: scriptPath}}
	m := NewManager(dir, servers)

	if c := m.clientForPath("/tmp/test.zzz"); c != nil {
		t.Fatal("clientForPath returned a client for a server that exits immediately")
	}
	if c := m.clientForPath("/tmp/test.zzz"); c != nil {
		t.Fatal("clientForPath returned a client on the second (should-be-cached) attempt")
	}

	data, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("counter file was never written — the script never ran even once: %v", err)
	}
	count := len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	if count != 1 {
		t.Fatalf("fake language server was invoked %d times for two clientForPath calls within the cooldown, want 1", count)
	}

	// Sanity: languageIDForExt keys failedStarts, so confirm the entry
	// landed under the extension's language ID, not something else.
	langID := languageIDForExt("zzz")
	m.mu.Lock()
	last, failed := m.failedStarts[langID]
	m.mu.Unlock()
	if !failed {
		t.Fatal("failedStarts has no entry after a failed start")
	}
	if time.Since(last) > startRetryCooldown {
		t.Fatal("recorded failure timestamp is already outside the cooldown window")
	}
}
