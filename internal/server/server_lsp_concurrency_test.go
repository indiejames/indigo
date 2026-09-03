package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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

// TestFormatDiscardsStaleResultAfterBufferSwap is a regression test: version
// alone is not enough to detect a stale Format result, because
// document.New() always starts a fresh buffer at version 0. If entry.buf is
// replaced by a brand-new *document.Buffer while an earlier Format call is
// still running (e.g. a second, faster concurrent Format call finishing
// first), the new buffer's version can coincidentally equal the first
// call's baseVersion despite being a completely different object — a
// version-only check would then let the first call's stale result clobber
// the second call's already-committed one. Format must also require the
// buffer pointer itself to be unchanged. See the entry.buf == baseBuf check
// in Format (server_lsp.go).
func TestFormatDiscardsStaleResultAfterBufferSwap(t *testing.T) {
	dir := t.TempDir()
	startedA := filepath.Join(dir, "started-a")
	releaseA := filepath.Join(dir, "release-a")
	// Formatter A: signals it has started, then blocks until told to
	// proceed, then uppercases — simulating a slow format on the buffer's
	// original (version 0) content.
	scriptA := filepath.Join(dir, "formatter-a.sh")
	if err := os.WriteFile(scriptA, []byte(
		"#!/bin/sh\ntouch "+startedA+"\nwhile [ ! -f "+releaseA+" ]; do sleep 0.02; done\ntr a-z A-Z\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Formatters: []config.FormatterConfig{
			{Extensions: []string{"swap"}, Command: scriptA},
		},
	}
	fmtMgr := format.NewManager(nil, cfg, dir)

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.swap", "hello\n")},
		},
		fmtMgr:    fmtMgr,
		lspMgr:    &lsp.Manager{},
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	// Start the slow Format call A; it will block until releaseA appears.
	formatADone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Format(ctx, func(p proto.EditorService_format_Params) error {
			p.SetBufId(1)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		formatADone <- err
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedA); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("formatter A never started (marker file never appeared)")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Format A is now genuinely blocked, holding baseVersion=0 from the
	// original buffer object. Swap the formatter to one that runs
	// instantly and produces different output, then run Format again
	// (call B) to completion — this installs a brand-new *document.Buffer
	// (also version 0) before A ever resumes.
	cfg.Formatters[0].Command = "sh"
	cfg.Formatters[0].Args = []string{"-c", "rev"}
	fut, rel := client.Format(context.Background(), func(p proto.EditorService_format_Params) error {
		p.SetBufId(1)
		return nil
	})
	_, err := fut.Struct()
	rel()
	if err != nil {
		t.Fatalf("Format call B errored: %v", err)
	}
	const wantAfterB = "olleh\n" // rev reverses each line's characters, keeping the trailing newline
	if got := s.buffers[1].buf.Content(); got != wantAfterB {
		t.Fatalf("buffer content after Format B = %q, want %q", got, wantAfterB)
	}
	bufAfterB := s.buffers[1].buf

	// Now let formatter A finish and commit (or, pre-fix, incorrectly
	// overwrite B's result).
	if err := os.WriteFile(releaseA, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := <-formatADone; err != nil {
		t.Fatalf("Format call A errored: %v", err)
	}

	if got := s.buffers[1].buf; got != bufAfterB {
		t.Fatalf("entry.buf changed after Format A's stale write — Format A clobbered Format B's buffer despite matching version")
	}
	if got := s.buffers[1].buf.Content(); got != wantAfterB {
		t.Fatalf("buffer content = %q, want %q — Format A's stale result (from the pre-swap buffer) must not overwrite Format B's already-committed buffer", got, wantAfterB)
	}
}

// TestLSPCallsDoNotBlockApplyOp is a regression test for a live bug report:
// a burst of external file changes (e.g. `git rebase`) can make a language
// server slow to respond. Without call.Go(), capnp serializes every other
// RPC on the connection behind whichever call is currently running — so a
// single slow LSP call would freeze a buffer switch's OpenFile, and (worse)
// an edit's ApplyOp plus the resync it triggers on failure, exactly
// matching the reported "loading..." hang followed by "resync failed."
// Exercises Hover as a representative case — the fix (call.Go()) is
// identical and mechanical across all 13 affected handlers in
// server_lsp.go — against a real TCP-backed lsp.Client/lsp.Manager talking
// to a fake, hand-framed LSP server that delays its hover response until
// signaled. The Content-Length framing is hand-rolled here (rather than
// reusing internal/lsp's own test helpers) because those are unexported in
// a different package; see internal/lsp/format_method_not_found_test.go
// for the sibling pattern this mirrors.
func TestLSPCallsDoNotBlockApplyOp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	hoverRequested := make(chan struct{})
	releaseHover := make(chan struct{})
	go fakeSlowHoverServer(t, ln, hoverRequested, releaseHover)

	lspMgr := lsp.NewManager(t.TempDir(), []lsp.ServerConfig{
		{Extensions: []string{"slow"}, Address: ln.Addr().String()},
	})

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.slow", "hello\n")},
		},
		fmtMgr:    format.NewManager(nil, &config.Config{}, t.TempDir()),
		lspMgr:    lspMgr,
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	hoverDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.Hover(ctx, func(p proto.EditorService_hover_Params) error {
			p.SetBufId(1)
			p.SetLine(0)
			p.SetCol(0)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		hoverDone <- err
	}()

	select {
	case <-hoverRequested:
	case <-time.After(2 * time.Second):
		t.Fatal("fake server never received the hover request")
	}

	// Hover is now genuinely blocked inside the fake server. Fire a
	// concurrent ApplyOp on the same connection and confirm it isn't
	// queued behind it.
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
	_, err = fut.Struct()
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ApplyOp errored: %v", err)
	}
	// Fixed, this takes well under a millisecond; broken, it takes ~5s
	// (blocked in the capnp call queue behind Hover's context timeout).
	if elapsed > 500*time.Millisecond {
		t.Fatalf("ApplyOp took %v — blocked behind the in-flight Hover call (call.Go() missing?)", elapsed)
	}

	close(releaseHover)
	if err := <-hoverDone; err != nil {
		t.Fatalf("Hover errored: %v", err)
	}
}

// TestReferencesSurvivesBufferCloseDuringLSPCall is a regression test for a
// nil-pointer-dereference bug a review caught: References re-fetches
// s.buffers[bufID] after the (now potentially long, thanks to call.Go() no
// longer blocking other RPCs) s.lspMgr.References call returns, to build
// preview text for same-file results — but indexed it directly
// (s.buffers[bufID].buf) with no "found" check. If the last client
// disconnects and CloseBuffer deletes that bufID's entry while the LSP call
// is still in flight (e.g. the user closes the tab right after triggering
// Find References), that second lookup returns a nil *bufferEntry and
// dereferencing .buf on it panics. The fix re-checks the map's ok value and
// simply skips preview generation when the buffer is gone.
func TestReferencesSurvivesBufferCloseDuringLSPCall(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close() //nolint:errcheck

	referencesRequested := make(chan struct{})
	releaseReferences := make(chan struct{})
	go fakeSlowReferencesServer(t, ln, referencesRequested, releaseReferences)

	lspMgr := lsp.NewManager(t.TempDir(), []lsp.ServerConfig{
		{Extensions: []string{"slow"}, Address: ln.Addr().String()},
	})

	s := &editorService{
		buffers: map[uint32]*bufferEntry{
			1: {buf: document.New("/tmp/x.slow", "hello\n"), clients: map[uint64]struct{}{9: {}}},
		},
		fmtMgr:    format.NewManager(nil, &config.Config{}, t.TempDir()),
		lspMgr:    lspMgr,
		lintMgr:   lint.NewManager(&config.Config{}, t.TempDir()),
		pluginMgr: &plugin.Manager{},
	}
	client := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})

	referencesDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		fut, rel := client.References(ctx, func(p proto.EditorService_references_Params) error {
			p.SetBufId(1)
			p.SetLine(0)
			p.SetCol(0)
			return nil
		})
		defer rel()
		_, err := fut.Struct()
		referencesDone <- err
	}()

	select {
	case <-referencesRequested:
	case <-time.After(2 * time.Second):
		t.Fatal("fake server never received the references request")
	}

	// The LSP call is now genuinely in flight. Close the only client's
	// reference to the buffer, exactly as if the user closed the tab —
	// this deletes s.buffers[1] entirely.
	closeFut, closeRel := client.CloseBuffer(context.Background(), func(p proto.EditorService_closeBuffer_Params) error {
		p.SetClientId(9)
		p.SetBufferId(1)
		return nil
	})
	if _, err := closeFut.Struct(); err != nil {
		closeRel()
		t.Fatalf("CloseBuffer errored: %v", err)
	}
	closeRel()
	s.mu.Lock()
	_, stillPresent := s.buffers[1]
	s.mu.Unlock()
	if stillPresent {
		t.Fatal("test setup: buffer 1 should be gone after CloseBuffer")
	}

	// Now let the in-flight References call's LSP response arrive — pre-fix,
	// this panics trying to build a preview against the now-deleted buffer.
	close(releaseReferences)

	select {
	case err := <-referencesDone:
		if err != nil {
			t.Fatalf("References errored: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for References to return")
	}
}

// fakeSlowReferencesServer accepts one TCP connection and speaks just
// enough LSP to satisfy lsp.Client.Initialize and a single
// textDocument/references request: answers "initialize" immediately,
// ignores notifications, then on "textDocument/references" closes
// referencesRequested and blocks until releaseReferences is closed before
// answering with one location in the same file (/tmp/x.slow) — this is
// what exercises the preview-generation code path that used to panic.
func fakeSlowReferencesServer(t *testing.T, ln net.Listener, referencesRequested, releaseReferences chan struct{}) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck
	br := bufio.NewReader(conn)
	for {
		body, err := readFramedLSPTestMessage(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("fake server: unmarshal request: %v", err)
			return
		}
		if len(msg.ID) == 0 {
			continue // notification, no response expected
		}
		switch msg.Method {
		case "initialize":
			err = writeFramedLSPTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/references":
			close(referencesRequested)
			<-releaseReferences
			err = writeFramedLSPTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result": []map[string]any{
					{
						"uri": "file:///tmp/x.slow",
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
							"end":   map[string]any{"line": 0, "character": 5},
						},
					},
				},
			})
		default:
			t.Errorf("fake server: unexpected request method %q", msg.Method)
			return
		}
		if err != nil {
			t.Errorf("fake server: write response: %v", err)
			return
		}
	}
}

// fakeSlowHoverServer accepts one TCP connection and speaks just enough LSP
// to satisfy lsp.Client.Initialize and a single textDocument/hover request:
// answers "initialize" immediately, ignores notifications (no "id" field,
// e.g. "initialized"/"textDocument/didOpen" — no response expected), then
// on "textDocument/hover" closes hoverRequested and blocks until
// releaseHover is closed before answering with a null result (Client.Hover
// treats a literal JSON null identically to "no hover info", so this
// avoids needing to match the Hover/MarkupContent JSON shape exactly).
func fakeSlowHoverServer(t *testing.T, ln net.Listener, hoverRequested, releaseHover chan struct{}) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck
	br := bufio.NewReader(conn)
	for {
		body, err := readFramedLSPTestMessage(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Errorf("fake server: unmarshal request: %v", err)
			return
		}
		if len(msg.ID) == 0 {
			continue // notification, no response expected
		}
		switch msg.Method {
		case "initialize":
			err = writeFramedLSPTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/hover":
			close(hoverRequested)
			<-releaseHover
			err = writeFramedLSPTestMessage(conn, map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  nil,
			})
		default:
			t.Errorf("fake server: unexpected request method %q", msg.Method)
			return
		}
		if err != nil {
			t.Errorf("fake server: write response: %v", err)
			return
		}
	}
}

// readFramedLSPTestMessage/writeFramedLSPTestMessage are defined in
// lsp_workspace_scan_diagnostics_test.go and shared package-wide.
