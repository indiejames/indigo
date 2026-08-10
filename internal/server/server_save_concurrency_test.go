package server

import (
	"context"
	"os"
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

// TestSaveDoesNotBlockApplyOp is Save's counterpart to
// TestFormatDoesNotBlockApplyOp: format-on-save runs the same potentially
// slow synchronous format call Format does, so Save needs the same
// call.Go() to avoid freezing every other RPC on the connection (typing,
// in particular) behind it for the duration of the format.
func TestSaveDoesNotBlockApplyOp(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	filePath := filepath.Join(dir, "x.slow")
	cfg := &config.Config{
		FormatOnSave: true,
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "touch " + startedPath + " && sleep 2 && cat"}},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, dir)

	s := &editorService{
		cfg: cfg,
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(filePath, "hello\n")},
		},
		fmtMgr:      fmtMgr,
		lspMgr:      &lsp.Manager{},
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	saveDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Save(ctx, func(p proto.EditorService_save_Params) error {
			p.SetBufferId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		saveDone <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("formatter never started (marker file never appeared)")
		}
		time.Sleep(5 * time.Millisecond)
	}

	start := time.Now()
	fut, rel := client.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
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
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ApplyOp errored: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ApplyOp took %v — blocked behind the in-flight Save call (call.Go() missing?)", elapsed)
	}

	if err := <-saveDone; err != nil {
		t.Fatalf("Save errored: %v", err)
	}
}

// TestSaveDiscardsStaleFormatResultOnConcurrentEdit is Save's counterpart to
// TestFormatDiscardsStaleResultOnConcurrentEdit: format-on-save formats the
// buffer outside the lock, which can take a while. Without a staleness
// check on the result (and without re-reading the buffer's current content
// before writing to disk), a keystroke's ApplyOp landing while formatting
// is in flight gets silently clobbered both in memory and on disk.
func TestSaveDiscardsStaleFormatResultOnConcurrentEdit(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	filePath := filepath.Join(dir, "x.slow")
	cfg := &config.Config{
		FormatOnSave: true,
		Formatters: []config.FormatterConfig{
			// tr actually transforms the content (changed=true), unlike cat
			// in TestSaveDoesNotBlockApplyOp, which only tests timing.
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "touch " + startedPath + " && sleep 1 && tr a-z A-Z"}},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, dir)

	s := &editorService{
		cfg: cfg,
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New(filePath, "hello\n")},
		},
		fmtMgr:      fmtMgr,
		lspMgr:      &lsp.Manager{},
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	saveDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Save(ctx, func(p proto.EditorService_save_Params) error {
			p.SetBufferId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		saveDone <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("formatter never started (marker file never appeared)")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Formatting is genuinely in flight (mid-sleep). Apply a concurrent edit.
	fut, rel := client.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
		p.SetClientId(1)
		p.SetBufferId(1)
		op, err := p.NewOp()
		if err != nil {
			return err
		}
		op.SetType(proto.EditOp_OpType_insert)
		op.SetInsertLine(0)
		op.SetInsertCol(5)
		return op.SetInsertText(" world")
	})
	_, applyErr := fut.Struct()
	rel()
	if applyErr != nil {
		t.Fatalf("ApplyOp errored: %v", applyErr)
	}

	if err := <-saveDone; err != nil {
		t.Fatalf("Save errored: %v", err)
	}

	const want = "hello world\n"
	if got := s.buffers[1].buf.Content(); got != want {
		t.Fatalf("buffer content = %q, want %q — the concurrent insert must survive Save's stale (pre-edit) format result", got, want)
	}
	diskContent, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if string(diskContent) != want {
		t.Fatalf("disk content = %q, want %q — Save must write the buffer's current content, not the stale formatted result", string(diskContent), want)
	}
	if s.buffers[1].buf.Dirty() {
		t.Error("buffer should be marked clean: what's on disk matches the buffer's content at the time SetClean ran")
	}
}

// TestSaveAsHappyPath is a basic regression test for SaveAs — it had no
// direct tests at all before the staleness-guard fix (mirroring Save's)
// reworked its locking. Covers the ordinary rename: content lands at the
// new path, the buffer is repointed at it and marked clean, and canonPath
// is updated.
func TestSaveAsHappyPath(t *testing.T) {
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

	fut, rel := client.SaveAs(context.Background(), func(p proto.EditorService_saveAs_Params) error {
		p.SetBufferId(1)
		return p.SetPath(newPath)
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatalf("SaveAs errored: %v", err)
	}

	diskContent, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("reading %s: %v", newPath, err)
	}
	if string(diskContent) != "package main\n" {
		t.Fatalf("disk content = %q, want %q", string(diskContent), "package main\n")
	}
	entry := s.buffers[1]
	if entry.buf.Path() != newPath {
		t.Errorf("entry.buf.Path() = %q, want %q", entry.buf.Path(), newPath)
	}
	if entry.buf.Dirty() {
		t.Error("buffer should be marked clean after SaveAs")
	}
	if want := canonicalPath(newPath); entry.canonPath != want {
		t.Errorf("entry.canonPath = %q, want %q", entry.canonPath, want)
	}
}
