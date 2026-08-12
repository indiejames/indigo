package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestSaveAsBumpsGenerationAndGetUpdatesReflectsIt is a regression test:
// GetUpdates's since>=version check alone can't tell a client that its
// buffer object was replaced wholesale (SaveAs/format-on-save/
// DiscardRecovery/Format all reset version to 0 via a fresh document.New).
// SaveAs must bump entry.generation, and GetUpdates must echo the current
// value so a client polling with a stale generation can detect the swap.
func TestSaveAsBumpsGenerationAndGetUpdatesReflectsIt(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.go")
	newPath := filepath.Join(dir, "new.go")

	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(oldPath, "package main\n"), canonPath: canonicalPath(oldPath)},
		},
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	if s.buffers[1].generation != 0 {
		t.Fatalf("test setup: generation = %d, want 0 before any swap", s.buffers[1].generation)
	}

	fut, rel := client.SaveAs(context.Background(), func(p proto.EditorService_saveAs_Params) error {
		p.SetBufferId(1)
		return p.SetPath(newPath)
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatalf("SaveAs errored: %v", err)
	}

	if s.buffers[1].generation != 1 {
		t.Fatalf("entry.generation = %d, want 1 after SaveAs's buffer swap", s.buffers[1].generation)
	}

	// A client polling with sinceVersion=0 (matching the fresh buffer's
	// version after the swap) would see no ops and, without generation,
	// have no way to tell its old content is now stale.
	fut2, rel2 := client.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		p.SetSinceVersion(0)
		return nil
	})
	defer rel2()
	res, err := fut2.Struct()
	if err != nil {
		t.Fatalf("GetUpdates errored: %v", err)
	}
	if res.Generation() != 1 {
		t.Errorf("GetUpdates response generation = %d, want 1", res.Generation())
	}
}
