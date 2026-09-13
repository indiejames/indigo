package server

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/lint"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/plugin"
	proto "github.com/indiejames/indigo/internal/proto"
)

// These are -race regression tests for an unsynchronized-read bug: GetUpdates,
// ApplyOp and ApplyOps each took s.mu only long enough to look up the
// bufferEntry, then read entry.buf and/or entry.generation *after* unlocking —
// while all four wholesale-swap sites (Save's format-on-save branch, SaveAs,
// DiscardRecovery, Format) replace both fields under s.mu.
//
// raceIters is deliberately high: the window between the unlock and the read
// is a few instructions wide, so a low iteration count detects the bug only
// intermittently. At this count every one of these failed on every attempt
// against the pre-fix code.
const raceIters = 1200

// newRaceTestService builds a service holding one buffer for path, reachable
// through two independent capnp capabilities so calls can genuinely overlap: a
// single EditorService_ServerToClient serializes non-Go() methods on its own
// call queue, which would hide the very interleaving these tests need.
func newRaceTestService(t *testing.T, path string) (*editorService, proto.EditorService, proto.EditorService) {
	t.Helper()
	s := &editorService{
		cfg: &config.Config{},
		buffers: map[uint32]*bufferEntry{
			1: {
				buf:           document.New(path, "package main\n"),
				canonPath:     canonicalPath(path),
				clients:       map[uint64]struct{}{1: {}},
				sinceByClient: map[uint64]uint64{},
			},
		},
		lspMgr:      lsp.NewManager(filepath.Dir(path), nil),
		lintMgr:     &lint.Manager{},
		pluginMgr:   &plugin.Manager{},
		dirWatches:  make(map[string]int),
		savingPaths: make(map[string]time.Time),
	}
	return s,
		proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1}),
		proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 2})
}

// swapBufferRepeatedly stands in for the four production swap sites: it does
// exactly what they do to the two fields under test — replace entry.buf and
// bump entry.generation, under s.mu — without their compare-and-swap guard.
//
// That guard is why the real RPCs can't be used as the writer here. It refuses
// the swap whenever the buffer changed under it, so against a reader that is
// itself editing (ApplyOp/ApplyOps) a real SaveAs almost always rejects and
// never reaches the assignment, leaving nothing for the race detector to pair
// the read with. What's under test is the locking discipline on these fields,
// and this writer exercises exactly that.
// bumpGeneration mirrors what the production sites do alongside the swap. It
// is a parameter because ApplyOp reads entry.generation only while holding
// s.mu (both for its check and for the rejection log), so entry.buf is its
// sole unlocked read — and a swapper that bumps the generation makes every
// call bail out at the check before it ever reaches that read, hiding the bug
// rather than exposing it.
func swapBufferRepeatedly(s *editorService, path string, iters int, bumpGeneration bool) {
	for i := 0; i < iters; i++ {
		s.mu.Lock()
		if e, ok := s.buffers[1]; ok {
			e.buf = document.New(path, "package main\n")
			if bumpGeneration {
				e.generation++
			}
		}
		s.mu.Unlock()
	}
}

// TestGetUpdatesNoRaceWithConcurrentSaveAs drives the reader against a real
// concurrent SaveAs. GetUpdates doesn't mutate the buffer, so SaveAs's
// compare-and-swap does land here and this exercises the whole production path
// end to end.
func TestGetUpdatesNoRaceWithConcurrentSaveAs(t *testing.T) {
	dir := t.TempDir()
	_, poller, saver := newRaceTestService(t, filepath.Join(dir, "a.go"))
	newPath := filepath.Join(dir, "b.go")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < raceIters; i++ {
			fut, rel := poller.GetUpdates(context.Background(), func(p proto.EditorService_getUpdates_Params) error {
				p.SetBufferId(1)
				p.SetClientId(1)
				p.SetSinceVersion(0)
				return nil
			})
			fut.Struct() //nolint:errcheck
			rel()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < raceIters; i++ {
			fut, rel := saver.SaveAs(context.Background(), func(p proto.EditorService_saveAs_Params) error {
				p.SetBufferId(1)
				return p.SetPath(newPath)
			})
			fut.Struct() //nolint:errcheck
			rel()
		}
	}()
	wg.Wait()
}

// TestApplyOpNoRaceWithConcurrentBufferSwap covers ApplyOp, which read
// entry.buf both for the rejection log's path and for the apply itself after
// releasing s.mu.
func TestApplyOpNoRaceWithConcurrentBufferSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	s, editor, _ := newRaceTestService(t, path)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		swapBufferRepeatedly(s, path, raceIters, false)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < raceIters; i++ {
			fut, rel := editor.ApplyOp(context.Background(), func(p proto.EditorService_applyOp_Params) error {
				p.SetBufferId(1)
				p.SetClientId(1)
				// Matches the entry's generation, which this test's swapper
				// deliberately leaves alone — see swapBufferRepeatedly.
				p.SetGeneration(0)
				op, err := p.NewOp()
				if err != nil {
					return err
				}
				op.SetType(proto.EditOp_OpType_insert)
				op.SetInsertLine(0)
				op.SetInsertCol(0)
				return op.SetInsertText("x")
			})
			fut.Struct() //nolint:errcheck
			rel()
		}
	}()
	wg.Wait()
}

// TestApplyOpsNoRaceWithConcurrentBufferSwap covers ApplyOps, which read
// entry.buf for the path, for every op's Apply, and for the trailing
// Content() — all after unlocking. Unlike ApplyOp it has no generation check,
// so every call here reaches the apply.
func TestApplyOpsNoRaceWithConcurrentBufferSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	s, editor, _ := newRaceTestService(t, path)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		swapBufferRepeatedly(s, path, raceIters, true)
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < raceIters; i++ {
			fut, rel := editor.ApplyOps(context.Background(), func(p proto.EditorService_applyOps_Params) error {
				p.SetBufferId(1)
				p.SetClientId(1)
				list, err := p.NewOps(1)
				if err != nil {
					return err
				}
				op := list.At(0)
				op.SetType(proto.EditOp_OpType_insert)
				op.SetInsertLine(0)
				op.SetInsertCol(0)
				return op.SetInsertText("y")
			})
			fut.Struct() //nolint:errcheck
			rel()
		}
	}()
	wg.Wait()
}
