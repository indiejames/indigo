package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/format"
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

	// GetBufferSnapshot is what a client actually resyncs with — keyed by
	// bufferID rather than path, so it must report the buffer's new path
	// too (not just content/version/generation), for a client whose own
	// remembered path is now stale after this rename.
	fut3, rel3 := client.GetBufferSnapshot(context.Background(), func(p proto.EditorService_getBufferSnapshot_Params) error {
		p.SetBufferId(1)
		return nil
	})
	defer rel3()
	res3, err := fut3.Struct()
	if err != nil {
		t.Fatalf("GetBufferSnapshot errored: %v", err)
	}
	content, err := res3.Content()
	if err != nil {
		t.Fatalf("res3.Content(): %v", err)
	}
	if content != "package main\n" {
		t.Errorf("GetBufferSnapshot content = %q, want %q", content, "package main\n")
	}
	if res3.Generation() != 1 {
		t.Errorf("GetBufferSnapshot generation = %d, want 1", res3.Generation())
	}
	path, err := res3.Path()
	if err != nil {
		t.Fatalf("res3.Path(): %v", err)
	}
	if path != newPath {
		t.Errorf("GetBufferSnapshot path = %q, want %q (the post-rename path, not the client's stale one)", path, newPath)
	}
}

// TestGetBufferSnapshotUnknownBufferErrors verifies the error path.
func TestGetBufferSnapshotUnknownBufferErrors(t *testing.T) {
	s := &editorService{buffers: map[uint32]*bufferEntry{}}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.GetBufferSnapshot(context.Background(), func(p proto.EditorService_getBufferSnapshot_Params) error {
		p.SetBufferId(99)
		return nil
	})
	defer rel()
	if _, err := fut.Struct(); err == nil {
		t.Error("expected an error for an unknown buffer, got nil")
	}
}

// TestFormatBumpsGenerationAndReturnsIt is a regression test: Format is one
// of the four sites that wholesale-swaps entry.buf (alongside SaveAs,
// DiscardRecovery, and Save's format-on-save branch — see this file's
// other tests), but unlike those, its capnp result carried no generation
// field at all until this fix. A client that formats its own buffer and
// applies the returned content locally had no way to update its remembered
// generation, so its very next GetUpdates poll would see the server's
// bumped value, mistake its own change for a foreign swap, and trigger an
// unnecessary resync (surfaced to the user as a "Buffer resynced from
// server" severe-error modal). Format must report the post-swap
// generation, matching SaveAs/DiscardRecovery/GetUpdates/GetBufferSnapshot.
func TestFormatBumpsGenerationAndReturnsIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")

	cfg := &config.Config{
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"go"}, Command: "tr", Args: []string{"a-z", "A-Z"}},
		},
	}
	s := &editorService{
		cfg: cfg,
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, "hello\n")},
		},
		fmtMgr:      format.NewManager(nil, cfg, dir),
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

	fut, rel := client.Format(context.Background(), func(p proto.EditorService_format_Params) error {
		p.SetBufId(1)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !res.Changed() {
		t.Fatal("test setup: expected a change (tr a-z A-Z uppercases \"hello\\n\") — check the fake formatter command")
	}
	if s.buffers[1].generation != 1 {
		t.Fatalf("entry.generation = %d, want 1 after Format's buffer swap", s.buffers[1].generation)
	}
	if res.Generation() != 1 {
		t.Errorf("Format response generation = %d, want 1 (matching entry.generation)", res.Generation())
	}
}

// TestApplyOpRejectsStaleGeneration is a regression test: ApplyOp had no
// staleness check at all, so a client unaware of a wholesale buffer swap
// (format-on-save/SaveAs/DiscardRecovery/Format) could send an op whose
// coordinates were computed against the old content, silently corrupting
// the new buffer at the wrong position instead of being told to resync.
func TestApplyOpRejectsStaleGeneration(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.go", "hello\n"), generation: 2},
		},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		p.SetGeneration(1) // stale — the buffer is actually at generation 2
		op, err := p.NewOp()
		if err != nil {
			return err
		}
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(0)
		return op.SetInsertText("x")
	})
	defer rel()
	_, err := fut.Struct()
	if err == nil {
		t.Fatal("expected ApplyOp to reject a stale generation, got nil error")
	}
	if got := s.buffers[1].buf.Content(); got != "hello\n" {
		t.Errorf("buffer content = %q, want unchanged %q — the stale op must not be applied", got, "hello\n")
	}
}

// TestApplyOpAcceptsMatchingGeneration is the happy path.
func TestApplyOpAcceptsMatchingGeneration(t *testing.T) {
	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.go", "hello\n"), generation: 2},
		},
		lspMgr:    lsp.NewManager("/tmp", nil),
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	fut, rel := client.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		p.SetGeneration(2) // matches
		op, err := p.NewOp()
		if err != nil {
			return err
		}
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(0)
		return op.SetInsertText("x")
	})
	defer rel()
	if _, err := fut.Struct(); err != nil {
		t.Fatalf("ApplyOp with matching generation errored: %v", err)
	}
	if got := s.buffers[1].buf.Content(); got != "xhello\n" {
		t.Errorf("buffer content = %q, want %q", got, "xhello\n")
	}
}
