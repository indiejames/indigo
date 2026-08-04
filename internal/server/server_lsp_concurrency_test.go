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
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// TestFormatDoesNotBlockApplyOp is a regression test for a bug where saving
// a file failed with "EditorService.applyOp: context deadline exceeded"
// while format-on-save was formatting. capnp serializes every RPC on a
// connection behind whichever call is currently running unless that call
// invokes call.Go() to hand the queue off to another goroutine. Format runs
// a formatter that can take seconds (an external command here, an LSP
// server's textDocument/formatting round trip in production); without
// call.Go() every other call on the connection — including ApplyOp for
// keystrokes typed while formatting is in flight — queues up behind it and
// can hit its own, shorter deadline first. See Format in server_lsp.go.
func TestFormatDoesNotBlockApplyOp(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	cfg := &config.Config{
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "touch " + startedPath + " && sleep 2 && cat"}},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, dir)

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.slow", "hello\n")},
		},
		fmtMgr:    fmtMgr,
		lspMgr:    &lsp.Manager{},
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	formatDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Format(ctx, func(p proto.EditorService_format_Params) error {
			p.SetBufId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		formatDone <- err
	}()

	// Wait for the formatter to actually be running (not a fixed sleep,
	// which could fire ApplyOp before Format has even been dispatched into
	// the capnp call queue — proving nothing either way) before firing the
	// edit it's supposed not to block.
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

	// context.Background(): the local capnp client used by this test doesn't
	// enforce ctx cancellation the way a real rpc.Conn does, so there's no
	// point setting a deadline here — see the elapsed-time check below.
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
	// Fixed, this takes well under a millisecond; broken, it takes ~1.8s
	// (blocked in the capnp call queue behind Format's 2s formatter).
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ApplyOp took %v — blocked behind the in-flight Format call (call.Go() missing?)", elapsed)
	}

	if err := <-formatDone; err != nil {
		t.Fatalf("Format errored: %v", err)
	}
}

// TestFormatDiscardsStaleResultOnConcurrentEdit is a regression test for a
// data-loss bug that call.Go() (see above) exposed: Format reads the buffer,
// then formats it outside the lock — which can take a while — before
// writing the result back. Without a staleness check, a keystroke's
// ApplyOp landing on the buffer while formatting is in flight (e.g. during
// format-on-save) gets silently clobbered when Format's result, computed
// from the buffer's older content, overwrites entry.buf. See the
// buf.Version() check in Format (server_lsp.go).
func TestFormatDiscardsStaleResultOnConcurrentEdit(t *testing.T) {
	dir := t.TempDir()
	startedPath := filepath.Join(dir, "started")
	cfg := &config.Config{
		Formatters: []config.FormatterConfig{
			// tr actually transforms the content (changed=true), unlike cat
			// in TestFormatDoesNotBlockApplyOp, which only tests timing.
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "touch " + startedPath + " && sleep 1 && tr a-z A-Z"}},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, dir)

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.slow", "hello\n")},
		},
		fmtMgr:    fmtMgr,
		lspMgr:    &lsp.Manager{},
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	formatDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Format(ctx, func(p proto.EditorService_format_Params) error {
			p.SetBufId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		formatDone <- err
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

	if err := <-formatDone; err != nil {
		t.Fatalf("Format errored: %v", err)
	}

	const want = "hello world\n"
	if got := s.buffers[1].buf.Content(); got != want {
		t.Fatalf("buffer content = %q, want %q — the concurrent insert must survive Format's stale (pre-edit) result", got, want)
	}
}
