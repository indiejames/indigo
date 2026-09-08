package agenttools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifySavedReportsDiskMismatch covers the guard added for a reported
// failure that could not be reproduced: apply_edits/save_file returned
// success while the file on disk never changed (CLAUDE.md, 2026-08-17).
//
// The root cause is still unknown — the server's own
// OpenFile/ApplyOps/Save/CloseBuffer sequence is proven to persist (see
// internal/server's TestMCPEditSequencePersistsToDisk) — so this does not fix
// it. What it does is make the bad state impossible to report as success, so
// a recurrence surfaces immediately instead of silently.
func TestVerifySavedReportsDiskMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	t.Run("disk matches the buffer", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("new content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := verifySavedAgainst("new content\n", nil, path); got != "" {
			t.Errorf("warning = %q, want none when disk matches the buffer", got)
		}
	})

	t.Run("disk still holds the old bytes", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("old content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := verifySavedAgainst("new content\n", nil, path)
		if got == "" {
			t.Fatal("no warning when the file on disk never changed — this is exactly the " +
				"state that was previously reported as success")
		}
		if !strings.Contains(got, "NOT changed on disk") {
			t.Errorf("warning = %q, want it to say plainly that the file did not change", got)
		}
	})

	t.Run("file unreadable after saving", func(t *testing.T) {
		if got := verifySavedAgainst("x", nil, filepath.Join(dir, "missing.txt")); got == "" {
			t.Error("no warning when the file could not be read back")
		}
	})

	t.Run("snapshot unavailable admits it was not verified", func(t *testing.T) {
		got := verifySavedAgainst("", errors.New("rpc down"), path)
		if !strings.Contains(got, "could not verify") {
			t.Errorf("warning = %q, want it to admit the write was not verified rather than "+
				"implying it was", got)
		}
	})
}
