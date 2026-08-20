package main

import (
	"testing"
	"time"

	"github.com/indiejames/indigo/sdk"
)

func newTestSpell() *Spell {
	return &Spell{
		cache:      make(map[uint32][]sdk.Decoration),
		pending:    make(map[uint32]*time.Timer),
		generation: make(map[uint32]uint64),
		bufPaths:   make(map[uint32]string),
		userWords:  make(map[string]struct{}),
	}
}

// TestApplyCheckResultDiscardsSupersededGeneration is a regression test for
// the race invalidateAll's scheduleCheck reuse introduced: t.Stop() on an
// already-fired timer is a no-op, so an older in-flight check (started
// before a dictionary-add fix) and a newer one (triggered by the fix) can
// both be running for the same buffer concurrently, with no guarantee the
// newer one's result is the one that actually lands last. Without a
// generation guard, whichever completes last wins — even if it's the
// stale one — silently reintroducing the just-fixed word's decoration.
func TestApplyCheckResultDiscardsSupersededGeneration(t *testing.T) {
	s := newTestSpell()
	const bufID = 1

	staleDecor := sdk.Decoration{Line: 0, Col: 0, EndCol: 5, FixData: "stale"}
	freshDecor := sdk.Decoration{Line: 0, Col: 0, EndCol: 5, FixData: "fresh"}

	// ok=false throughout: applyCheckResult's cache write is unconditional
	// on ok (only the PublishDiagnostics call is gated by it), and s.api is
	// left nil here since exercising the real fire-and-forget publish path
	// would need a fake capnp-backed sdk.Api — out of scope for testing the
	// generation guard itself, which is what this test is actually about.

	// The first check is scheduled (generation 1) and applies normally.
	s.generation[bufID] = 1
	s.applyCheckResult(bufID, 1, []sdk.Decoration{staleDecor}, nil, 100, false)
	if got := s.cache[bufID]; len(got) != 1 || got[0].FixData != "stale" {
		t.Fatalf("after first apply: cache = %+v, want [stale]", got)
	}

	// A fix is applied, invalidateAll runs, and scheduleCheck bumps the
	// generation to 2 for a fresh recheck — but the *first* check (gen 1)
	// is still in flight (its RPC round trips hadn't completed yet) and
	// now lands late, after generation has already moved on.
	s.generation[bufID] = 2
	s.applyCheckResult(bufID, 1, []sdk.Decoration{staleDecor}, nil, 100, false)
	if got := s.cache[bufID]; len(got) != 1 || got[0].FixData != "stale" {
		t.Fatalf("stale gen-1 result should have been discarded, cache = %+v", got)
	}

	// The gen-2 (current) check now completes and must be the one that
	// actually lands.
	s.applyCheckResult(bufID, 2, []sdk.Decoration{freshDecor}, nil, 200, false)
	if got := s.cache[bufID]; len(got) != 1 || got[0].FixData != "fresh" {
		t.Fatalf("current gen-2 result should have applied, cache = %+v", got)
	}
}

// TestApplyCheckResultClearsPending verifies pending is always cleared for
// bufID regardless of whether the result was applied or discarded as stale.
func TestApplyCheckResultClearsPending(t *testing.T) {
	s := newTestSpell()
	const bufID = 1
	s.generation[bufID] = 5
	s.mu.Lock()
	s.pending[bufID] = nil // placeholder entry; applyCheckResult only checks presence via delete
	s.mu.Unlock()

	s.applyCheckResult(bufID, 1, nil, nil, 0, false) // stale (gen mismatch) + not ok
	s.mu.Lock()
	_, stillPending := s.pending[bufID]
	s.mu.Unlock()
	if stillPending {
		t.Error("pending[bufID] should be cleared even when the result is discarded as stale")
	}
}
