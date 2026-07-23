package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	proto "github.com/indiejames/indigo/internal/proto"
)

func TestCanonicalPathResolvesSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.ts")
	if err := os.WriteFile(real, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.ts")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}
	// t.TempDir() itself may live under a symlink (e.g. macOS's /var ->
	// /private/var), so resolve the "real" path too before comparing.
	wantReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	if got := canonicalPath(link); got != wantReal {
		t.Errorf("canonicalPath(%q) = %q, want %q", link, got, wantReal)
	}
	if got := canonicalPath(real); got != wantReal {
		t.Errorf("canonicalPath(%q) = %q, want %q", real, got, wantReal)
	}
}

func TestCanonicalPathNonexistentFallsBackToInput(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.ts")
	if got := canonicalPath(missing); got != missing {
		t.Errorf("canonicalPath(%q) = %q, want unchanged", missing, got)
	}
	if got := canonicalPath(""); got != "" {
		t.Errorf("canonicalPath(\"\") = %q, want \"\"", got)
	}
}

// TestOpenFileDedupsSymlinkEquivalentPaths is a regression test for a bug
// report: opening a TypeScript file once via its literal (symlinked) path
// and once via a language-server-reported path that resolves the symlink
// (tsserver does this by default) produced two independent buffers for the
// same underlying file, with independently diverging dirty state instead of
// sharing one buffer.
func TestOpenFileDedupsSymlinkEquivalentPaths(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.ts")
	if err := os.WriteFile(real, []byte("export const x = 1;\n"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.ts")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported in this environment: %v", err)
	}

	recDir := t.TempDir()
	svc := newEditorService(recDir, dir, &config.Config{}, func() {}, nil)
	t.Cleanup(func() { svc.lspMgr.Shutdown() })
	client := proto.EditorService_ServerToClient(svc)
	ctx := context.Background()

	open := func(path string) uint32 {
		t.Helper()
		fut, release := client.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
			p.SetClientId(1)
			return p.SetPath(path)
		})
		defer release()
		res, err := fut.Struct()
		if err != nil {
			t.Fatalf("OpenFile(%q): %v", path, err)
		}
		return res.BufferId()
	}

	firstID := open(link)
	secondID := open(real)

	if secondID != firstID {
		t.Errorf("OpenFile(%q) returned bufferId=%d, OpenFile(%q) returned bufferId=%d; want the same buffer for symlink-equivalent paths", link, firstID, real, secondID)
	}
	if got := len(svc.buffers); got != 1 {
		t.Errorf("len(svc.buffers) = %d, want 1 (opening symlink-equivalent paths should not create a second buffer)", got)
	}
}
