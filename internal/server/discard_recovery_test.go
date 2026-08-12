package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/indiejames/indigo/internal/document"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestDiscardRecoveryHappyPath is a basic regression test for
// DiscardRecovery — it had no tests at all before the staleness-guard fix
// (mirroring Save/SaveAs/Format's own compare-and-swap check, which
// DiscardRecovery was missing) reworked its locking. Covers the ordinary
// case: content reloads from disk, the recovery file is removed, and
// generation is bumped for the buffer-object swap.
func TestDiscardRecoveryHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	if err := os.WriteFile(path, []byte("on disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recDir := t.TempDir()
	recPath := recoveryFilePath(recDir, path)
	if err := os.WriteFile(recPath, []byte("recovered content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &editorService{
		recDir: recDir,
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "recovered content\n")},
		},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.DiscardRecovery(context.Background(), func(p proto.EditorService_discardRecovery_Params) error {
		p.SetBufferId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("DiscardRecovery errored: %v", err)
	}
	content, err := res.Content()
	if err != nil {
		t.Fatalf("res.Content(): %v", err)
	}
	if content != "on disk\n" {
		t.Errorf("content = %q, want %q", content, "on disk\n")
	}
	if s.buffers[1].buf.Content() != "on disk\n" {
		t.Errorf("buffer content = %q, want %q", s.buffers[1].buf.Content(), "on disk\n")
	}
	if s.buffers[1].generation != 1 {
		t.Errorf("generation = %d, want 1", s.buffers[1].generation)
	}
	if _, err := os.Stat(recPath); !os.IsNotExist(err) {
		t.Error("recovery file should have been removed")
	}
}

// TestDiscardRecoveryUnknownBufferErrors verifies the error path.
func TestDiscardRecoveryUnknownBufferErrors(t *testing.T) {
	s := &editorService{buffers: map[uint32]*bufferEntry{}, recDir: t.TempDir()}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.DiscardRecovery(context.Background(), func(p proto.EditorService_discardRecovery_Params) error {
		p.SetBufferId(99)
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err == nil {
		t.Error("expected an error for an unknown buffer, got nil")
	}
}
