package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"

	"github.com/indiejames/indigo/internal/format"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	"github.com/indiejames/indigo/internal/proto"
)

// TestMCPEditSequencePersistsToDisk reproduces the exact RPC sequence the
// indigo-claude MCP tools use for apply_edits when nothing else has the file
// open: OpenFile -> BufferClientCount -> ApplyOps -> Save -> CloseBuffer.
//
// CLAUDE.md documents that this reported success while the change never
// reached disk, root cause uninvestigated. This drives the real server so the
// claim can be confirmed or ruled out rather than reasoned about.
func TestMCPEditSequencePersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &editorService{
		cfg:         &config.Config{},
		buffers:     map[uint32]*bufferEntry{},
		fmtMgr:      format.NewManager(nil, &config.Config{}, dir),
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
		recDir:      t.TempDir(),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. OpenFile
	ofFut, ofRel := cl.OpenFile(ctx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(1)
		return p.SetPath(path)
	})
	defer ofRel()
	ofRes, err := ofFut.Struct()
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	bufID := ofRes.BufferId()
	version := ofRes.Version()
	t.Logf("OpenFile -> bufID=%d version=%d generation=%d", bufID, version, ofRes.Generation())

	// 2. BufferClientCount — decides whether the plugin saves at all.
	bcFut, bcRel := cl.BufferClientCount(ctx, func(p proto.EditorService_bufferClientCount_Params) error {
		p.SetBufferId(bufID)
		return nil
	})
	defer bcRel()
	bcRes, err := bcFut.Struct()
	if err != nil {
		t.Fatalf("BufferClientCount: %v", err)
	}
	t.Logf("BufferClientCount -> %d (weOpened = %v)", bcRes.Count(), bcRes.Count() == 1)

	// 3. ApplyOps: delete "hello", insert "goodbye"
	aoFut, aoRel := cl.ApplyOps(ctx, func(p proto.EditorService_applyOps_Params) error {
		p.SetClientId(1)
		p.SetBufferId(bufID)
		list, err := p.NewOps(2)
		if err != nil {
			return err
		}
		del := list.At(0)
		del.SetType(proto.EditOp_OpType_delete)
		del.SetFromLine(0)
		del.SetFromCol(0)
		del.SetToLine(0)
		del.SetToCol(5)
		del.SetVersion(version)
		ins := list.At(1)
		ins.SetType(proto.EditOp_OpType_insert)
		ins.SetInsertLine(0)
		ins.SetInsertCol(0)
		return ins.SetInsertText("goodbye")
	})
	defer aoRel()
	aoRes, err := aoFut.Struct()
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	t.Logf("ApplyOps -> version=%d", aoRes.Version())

	// 4. Save
	svFut, svRel := cl.Save(ctx, func(p proto.EditorService_save_Params) error {
		p.SetBufferId(bufID)
		return nil
	})
	defer svRel()
	if _, err := svFut.Struct(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 5. CloseBuffer
	cbFut, cbRel := cl.CloseBuffer(ctx, func(p proto.EditorService_closeBuffer_Params) error {
		p.SetClientId(1)
		p.SetBufferId(bufID)
		return nil
	})
	defer cbRel()
	if _, err := cbFut.Struct(); err != nil {
		t.Fatalf("CloseBuffer: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("disk content after the full sequence: %q", string(got))
	if string(got) != "goodbye\n" {
		t.Errorf("disk = %q, want %q — the edit did not persist", string(got), "goodbye\n")
	}
}
