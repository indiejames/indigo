package server

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/proto"
)

// openFileFor drives one OpenFile RPC and returns the buffer id and content.
func openFileFor(t *testing.T, client proto.EditorService, clientID uint64, path string) (uint32, string, error) {
	t.Helper()
	fut, release := client.OpenFile(context.Background(), func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(clientID)
		return p.SetPath(path)
	})
	defer release()
	res, err := fut.Struct()
	if err != nil {
		return 0, "", err
	}
	content, err := res.Content()
	if err != nil {
		return 0, "", err
	}
	return res.BufferId(), content, nil
}

// TestOpenFileAttachesToOpenBufferWhenFileUnreadable is a regression test for
// a bug introduced alongside loadContent's new error return: the read happened
// *before* the already-open-buffer dedup check, so its error aborted the whole
// call. A second client (a second window, a plugin's RequestOpenFile, a
// reload) could no longer attach to a buffer that is open and perfectly usable
// in memory, just because the backing file had since become unreadable —
// even though the attach path never uses the freshly-read content anyway, it
// serves the in-memory buffer.
//
// Scoped to the unreadable case. The finding also named the deleted-file
// case, but that fails for a separate, pre-existing reason this fix does not
// touch: canonicalPath resolves symlinks while the file exists (on macOS
// /var/... -> /private/var/...) and falls back to the raw input once it's
// gone, so the canonPath recorded at first open can never match the one
// computed after deletion and dedup misses regardless. Recorded as its own
// finding in PLAN.md rather than asserted here, where it would fail for a
// reason unrelated to what this test is about.
func TestOpenFileAttachesToOpenBufferWhenFileUnreadable(t *testing.T) {
	for _, tc := range []struct {
		name   string
		breakF func(t *testing.T, path string)
	}{
		{"unreadable", func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("running as root: mode bits don't prevent reads")
			}
			if runtime.GOOS == "windows" {
				t.Skip("chmod-based unreadability is POSIX-specific")
			}
			if err := os.Chmod(path, 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, 0o644) }) //nolint:errcheck
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "shared.go")
			const original = "package shared\n"
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}

			svc := newEditorService(t.TempDir(), dir, &config.Config{}, func() {}, nil)
			t.Cleanup(func() { svc.lspMgr.Shutdown() })
			client := proto.EditorService_ServerToClient(svc)

			firstID, firstContent, err := openFileFor(t, client, 1, path)
			if err != nil {
				t.Fatalf("first OpenFile: %v", err)
			}
			if firstContent != original {
				t.Fatalf("first OpenFile content = %q, want %q", firstContent, original)
			}

			tc.breakF(t, path)

			// The buffer is still open and holds the content; a second client
			// must be able to attach to it.
			secondID, secondContent, err := openFileFor(t, client, 2, path)
			if err != nil {
				t.Fatalf("second OpenFile after file became %s: %v — a client should still be able to "+
					"attach to the already-open in-memory buffer", tc.name, err)
			}
			if secondID != firstID {
				t.Errorf("second OpenFile bufferId = %d, want %d (same buffer)", secondID, firstID)
			}
			if secondContent != original {
				t.Errorf("second OpenFile content = %q, want the in-memory %q", secondContent, original)
			}
			if got := len(svc.buffers); got != 1 {
				t.Errorf("len(svc.buffers) = %d, want 1", got)
			}
		})
	}
}

// TestOpenFileStillReportsUnreadableFileForNewBuffer pins the other half of
// the behavior: when there is no already-open buffer to attach to, an
// unreadable file must still surface the read error rather than silently
// opening an empty buffer (the original finding this all came from).
func TestOpenFileStillReportsUnreadableFileForNewBuffer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits don't prevent reads")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is POSIX-specific")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.go")
	if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) }) //nolint:errcheck

	svc := newEditorService(t.TempDir(), dir, &config.Config{}, func() {}, nil)
	t.Cleanup(func() { svc.lspMgr.Shutdown() })
	client := proto.EditorService_ServerToClient(svc)

	if _, content, err := openFileFor(t, client, 1, path); err == nil {
		t.Errorf("OpenFile on an unreadable file with no buffer open returned no error (content=%q); "+
			"it must not silently present an empty buffer", content)
	}
}
