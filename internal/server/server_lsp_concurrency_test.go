package server

import (
	"context"
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
	cfg := &config.Config{
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"slow"}, Command: "sh", Args: []string{"-c", "sleep 2 && cat"}},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, t.TempDir())

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.slow", "hello\n")},
		},
		fmtMgr:    fmtMgr,
		lspMgr:    &lsp.Manager{},
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	formatDone := make(chan struct{})
	go func() {
		defer close(formatDone)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Format(ctx, func(p proto.EditorService_format_Params) error {
			p.SetBufId(1)
			return nil
		})
		defer rel()
		fut.Struct() //nolint:errcheck
	}()

	// Give the Format call time to start (and, pre-fix, claim the queue)
	// before firing the edit it's supposed not to block.
	time.Sleep(200 * time.Millisecond)

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

	<-formatDone
}
