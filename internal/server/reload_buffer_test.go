package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// newReloadTestService builds a service holding one buffer for path with the
// given set of attached clients.
func newReloadTestService(t *testing.T, path, bufContent string, clientIDs ...uint64) (*editorService, proto.EditorService) {
	t.Helper()
	clients := make(map[uint64]struct{}, len(clientIDs))
	for _, id := range clientIDs {
		clients[id] = struct{}{}
	}
	dir := filepath.Dir(path)
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:           document.New(path, bufContent),
				canonPath:     canonicalPath(path),
				clients:       clients,
				sinceByClient: map[uint64]uint64{},
			},
		},
		recDir:      t.TempDir(),
		lspMgr:      lsp.NewManager(dir, nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	t.Cleanup(cl.Release)
	return s, cl
}

func callReload(t *testing.T, cl proto.EditorService) (content string, version, generation uint64, err error) {
	t.Helper()
	fut, rel := cl.ReloadBuffer(context.Background(), func(p proto.EditorService_reloadBuffer_Params) error {
		p.SetBufferId(1)
		p.SetClientId(1)
		return nil
	})
	defer rel()
	res, ferr := fut.Struct()
	if ferr != nil {
		return "", 0, 0, ferr
	}
	c, cerr := res.Content()
	if cerr != nil {
		return "", 0, 0, cerr
	}
	return c, res.Version(), res.Generation(), nil
}

// TestReloadBufferReadsDiskWithASecondClientAttached is the regression test for
// the bug this RPC exists to fix. Reloading used to be client-side CloseBuffer +
// OpenFile, which silently reloaded *nothing* whenever another window had the
// same file open: CloseBuffer only drops the calling client, so the entry
// survived with a non-empty client set, and OpenFile's attach path then served
// the same stale in-memory content straight back without ever reading disk.
//
// Two clients are attached here precisely because that is the case the old flow
// got wrong; the single-client case happened to work by accident.
func TestReloadBufferReadsDiskWithASecondClientAttached(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watched.go")
	if err := os.WriteFile(path, []byte("from disk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Clients 1 and 2 both hold the buffer, and its in-memory content differs
	// from what is now on disk (as after an external write).
	s, cl := newReloadTestService(t, path, "stale in memory\n", 1, 2)

	content, version, generation, err := callReload(t, cl)
	if err != nil {
		t.Fatalf("ReloadBuffer errored: %v", err)
	}
	if content != "from disk\n" {
		t.Errorf("returned content = %q, want %q — the reload must read the file, not the surviving buffer",
			content, "from disk\n")
	}

	s.mu.Lock()
	gotContent := s.buffers[1].buf.Content()
	gotGen := s.buffers[1].generation
	stillHeld := len(s.buffers[1].clients)
	s.mu.Unlock()

	if gotContent != "from disk\n" {
		t.Errorf("server buffer content = %q, want %q", gotContent, "from disk\n")
	}
	if gotGen != 1 || generation != gotGen {
		t.Errorf("generation = %d (returned %d), want 1 — the swap must bump it so other clients resync",
			gotGen, generation)
	}
	if version != 0 {
		t.Errorf("returned version = %d, want 0 (a fresh document.New)", version)
	}
	if stillHeld != 2 {
		t.Errorf("clients attached = %d, want 2 — reloading must not drop anyone's hold", stillHeld)
	}
}

// TestReloadBufferRejectsConcurrentEdit covers the compare-and-swap: an edit
// landing while the disk read is in flight must not be silently discarded.
//
// The buffer's file is a FIFO rather than a regular file, which is what makes
// this deterministic rather than a timing race. os.ReadFile on a FIFO blocks
// until a writer shows up, so the handler parks inside its read — after
// capturing baseVersion, before the swap check — for exactly as long as the
// test likes. CLAUDE.md notes that dedicated tests for the same
// compare-and-swap in Save/SaveAs/DiscardRecovery were skipped for want of "a
// slow/controllable step to reliably interleave a concurrent edit"; the disk
// read is that step, so this pattern covers the shape of guard those use too.
func TestReloadBufferRejectsConcurrentEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo unavailable on this platform: %v", err)
	}
	s, cl := newReloadTestService(t, path, "in memory\n", 1)

	done := make(chan error, 1)
	go func() {
		_, _, _, err := callReload(t, cl)
		done <- err
	}()

	// Opening a FIFO for writing with O_NONBLOCK fails with ENXIO until a
	// reader has it open, so this succeeding is proof the handler has captured
	// baseVersion and reached os.ReadFile — no sleeping and hoping.
	var w *os.File
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			w = f
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ReloadBuffer never reached its read: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Land the edit the reload must not throw away. The handler cannot proceed
	// past its read until the write below closes, so this is ordered before the
	// swap check with no reliance on scheduling.
	s.mu.Lock()
	buf := s.buffers[1].buf
	s.mu.Unlock()
	buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})

	if _, err := w.WriteString("from disk\n"); err != nil {
		t.Fatalf("writing to the FIFO: %v", err)
	}
	w.Close() //nolint:errcheck

	err := <-done
	if err == nil {
		t.Fatal("ReloadBuffer succeeded despite a concurrent edit; that edit would be silently discarded")
	}
	if !strings.Contains(err.Error(), "changed while reloading") {
		t.Errorf("error = %q, want it to mention the buffer changing while reloading", err)
	}

	s.mu.Lock()
	got := s.buffers[1].buf.Content()
	gen := s.buffers[1].generation
	s.mu.Unlock()
	if got != "xin memory\n" {
		t.Errorf("buffer content = %q, want %q — the concurrent edit must survive", got, "xin memory\n")
	}
	if gen != 0 {
		t.Errorf("generation = %d, want 0 — a rejected reload must not bump it", gen)
	}
}
