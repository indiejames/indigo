package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestMoveTextToFileNoRaceAgainstConcurrentBufferSwap and
// TestApplyWorkspaceEditsNoRaceAgainstConcurrentBufferSwap regression-test the
// same bug class Phase 4's CodeRabbit round already closed for ApplyOp: both
// MoveTextToFile (server_move_to_file.go) and ApplyWorkspaceEdits
// (server_workspace_edit.go) used to look up entry.buf under s.mu.Lock(),
// unlock, and only then read/mutate it — but entry.buf is a plain field that
// Save/SaveAs/Format/DiscardRecovery all wholesale-swap to a *new*
// document.Buffer object under s.mu.Lock() (e.g. a concurrent format-on-save
// or LSP rename). A swap landing in that unlocked window meant the handler's
// delete/insert coordinates, computed against the pre-swap content, got
// applied to the post-swap buffer object instead — silently corrupting it at
// the wrong offsets, and a genuine unsynchronized read/write data race on the
// entry.buf field regardless of whether the offsets happened to still be
// in-bounds. The fix holds s.mu across the whole find-then-apply sequence in
// both handlers (both do only pure in-memory work, so this is cheap).
//
// Each test races a tight goroutine that repeatedly swaps entry.buf exactly
// the way Save/SaveAs/Format/DiscardRecovery do (fresh document.Buffer +
// entry.generation++, both under s.mu.Lock()) against repeated calls to the
// handler under test. Both directly assert there's no unexpected error, and
// — the real point — must be run with `go test -race`: reverting either fix
// makes this test fail under -race with a clear data race on entry.buf.
func TestMoveTextToFileNoRaceAgainstConcurrentBufferSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.go")
	destPath := filepath.Join(dir, "dest.go")
	const content = "func f() {}\n"

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(srcPath, content), canonPath: canonicalPath(srcPath)},
		},
		lspMgr:      &lsp.Manager{},
		pluginMgr:   &plugin.Manager{},
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	const n = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			s.mu.Lock()
			if e := s.buffers[1]; e != nil {
				e.buf = document.New(srcPath, content)
				e.generation++
			}
			s.mu.Unlock()
		}
	}()

	for i := 0; i < n; i++ {
		fut, rel := client.MoveTextToFile(context.Background(), func(p proto.EditorService_moveTextToFile_Params) error {
			p.SetClientId(1)
			p.SetBufId(1)
			// A zero-length range: keeps srcPath's content stable at exactly
			// `content` across every iteration (no cumulative shrinking to
			// chase), so any given iteration's swap always leaves valid,
			// in-bounds coordinates. The race under test is on the entry.buf
			// field itself, not on the extracted text.
			p.SetFromLine(0)
			p.SetFromCol(0)
			p.SetToLine(0)
			p.SetToCol(0)
			return p.SetDestPath(destPath)
		})
		_, err := fut.Struct()
		rel()
		if err != nil {
			t.Fatalf("MoveTextToFile unexpected error on iteration %d: %v", i, err)
		}
	}
	<-done
}

func TestApplyWorkspaceEditsNoRaceAgainstConcurrentBufferSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "src.go")
	const content = "foo bar\n"

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(path, content), canonPath: canonicalPath(path)},
		},
		lspMgr:      &lsp.Manager{},
		pluginMgr:   &plugin.Manager{},
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	const n = 500
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			s.mu.Lock()
			if e := s.buffers[1]; e != nil {
				e.buf = document.New(path, content)
				e.generation++
			}
			s.mu.Unlock()
		}
	}()

	for i := 0; i < n; i++ {
		fut, rel := client.ApplyWorkspaceEdits(context.Background(), func(p proto.EditorService_applyWorkspaceEdits_Params) error {
			p.SetClientId(1)
			edits, err := p.NewEdits(1)
			if err != nil {
				return err
			}
			e := edits.At(0)
			if err := e.SetPath(path); err != nil {
				return err
			}
			e.SetLine(0)
			e.SetCol(0)
			if err := e.SetOldText("foo"); err != nil {
				return err
			}
			return e.SetNewText("foo") // no-op replacement, keeps content stable
		})
		_, err := fut.Struct()
		rel()
		if err != nil {
			t.Fatalf("ApplyWorkspaceEdits unexpected error on iteration %d: %v", i, err)
		}
	}
	<-done

	if !strings.HasPrefix(s.buffers[1].buf.Content(), "foo bar") {
		t.Errorf("buffer content = %q, want it to still start with %q", s.buffers[1].buf.Content(), "foo bar")
	}
}
