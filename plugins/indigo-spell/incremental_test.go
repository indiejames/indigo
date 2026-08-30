package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/client9/gospell"
	"github.com/indiejames/indigo/sdk"
)

func newTestSpellWithChecker(t *testing.T) *Spell {
	t.Helper()
	s := newTestSpell()
	checker, err := gospell.NewGoSpellReader(bytes.NewReader(affData), bytes.NewReader(dicData))
	if err != nil {
		t.Fatalf("load dictionary: %v", err)
	}
	s.checker = checker
	return s
}

// TestCommonPrefixSuffixLen covers the diff primitives diffAndCheck builds
// on: prefix/suffix length on identical, disjoint, and short slices, and
// that the suffix scan never crosses back into the prefix.
func TestCommonPrefixSuffixLen(t *testing.T) {
	tests := []struct {
		name             string
		a, b             []string
		wantPrefix       int
		wantSuffixNoPfx  int // commonSuffixLen(a, b, 0)
		wantSuffixWithPx int // commonSuffixLen(a, b, wantPrefix)
	}{
		{"identical", []string{"a", "b", "c"}, []string{"a", "b", "c"}, 3, 3, 0},
		{"disjoint", []string{"a", "b"}, []string{"x", "y"}, 0, 0, 0},
		{"empty a", nil, []string{"a", "b"}, 0, 0, 0},
		{"both empty", nil, nil, 0, 0, 0},
		{"prefix only", []string{"a", "b", "x"}, []string{"a", "b", "y"}, 2, 0, 0},
		{"suffix only", []string{"x", "a", "b"}, []string{"y", "a", "b"}, 0, 2, 2},
		{"whole-slice overlap capped by prefix", []string{"a"}, []string{"a"}, 1, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commonPrefixLen(tt.a, tt.b); got != tt.wantPrefix {
				t.Errorf("commonPrefixLen = %d, want %d", got, tt.wantPrefix)
			}
			if got := commonSuffixLen(tt.a, tt.b, 0); got != tt.wantSuffixNoPfx {
				t.Errorf("commonSuffixLen(prefix=0) = %d, want %d", got, tt.wantSuffixNoPfx)
			}
			if got := commonSuffixLen(tt.a, tt.b, tt.wantPrefix); got != tt.wantSuffixWithPx {
				t.Errorf("commonSuffixLen(prefix=%d) = %d, want %d", tt.wantPrefix, got, tt.wantSuffixWithPx)
			}
		})
	}
}

// TestDiffAndCheckOnlyRechecksChangedLine is a regression test for the core
// incremental-check claim: editing one line must not re-spell-check any
// other line. Lines 0 and 1 are seeded in the old per-line cache with
// deliberately fabricated ("poisoned") decorations that a real check of
// their actual (correctly-spelled) text would never produce; if
// diffAndCheck recomputed them instead of carrying the cached entries
// forward unchanged, the poison would disappear from the result.
func TestDiffAndCheckOnlyRechecksChangedLine(t *testing.T) {
	s := newTestSpellWithChecker(t)

	oldLines := []string{"hello there", "xyzzyqqqq nonsense", "goodbye now"}
	poison0 := sdk.Decoration{FixData: "poison-line0"}
	poison1 := sdk.Decoration{FixData: "poison-line1"}
	oldLineDecors := map[uint32][]sdk.Decoration{
		0: {poison0},
		1: {poison1},
	}
	oldLineDiags := map[uint32][]sdk.Diagnostic{}

	// Only line 2 changes, and its replacement contains a genuine misspelling.
	newLines := []string{"hello there", "xyzzyqqqq nonsense", "wrongspeling now"}

	lineDecors, _ := s.diffAndCheck("notes.txt", oldLines, newLines, oldLineDecors, oldLineDiags)

	if got := lineDecors[0]; len(got) != 1 || got[0].FixData != "poison-line0" {
		t.Errorf("line 0 (unchanged) = %+v, want carried-forward poison marker (not recomputed)", got)
	}
	if got := lineDecors[1]; len(got) != 1 || got[0].FixData != "poison-line1" {
		t.Errorf("line 1 (unchanged) = %+v, want carried-forward poison marker (not recomputed)", got)
	}
	got, ok := lineDecors[2]
	if !ok || len(got) == 0 {
		t.Fatalf("line 2 (changed) has no decorations, want a flagged misspelling for %q", "wrongspeling")
	}
	var payload fixPayload
	if err := json.Unmarshal([]byte(got[0].FixData), &payload); err != nil {
		t.Fatalf("unmarshal FixData: %v", err)
	}
	if payload.Word != "wrongspeling" {
		t.Errorf("line 2 flagged word = %q, want %q", payload.Word, "wrongspeling")
	}
}

// TestDiffAndCheckShiftsCarriedLinesOnInsertedLine verifies that when an
// edit inserts a line in the middle, unchanged lines before and after the
// insertion point are both carried forward from cache (not recomputed —
// again proven via a poison marker) and that the lines after the insertion
// point are correctly renumbered by the line-count delta.
func TestDiffAndCheckShiftsCarriedLinesOnInsertedLine(t *testing.T) {
	s := newTestSpellWithChecker(t)

	oldLines := []string{"one", "two", "three", "four"}
	poisonFirst := sdk.Decoration{FixData: "poison-first"}
	poisonLast := sdk.Decoration{FixData: "poison-last"}
	oldLineDecors := map[uint32][]sdk.Decoration{
		0: {poisonFirst},
		3: {poisonLast}, // attached to "four"
	}

	// Insert a new (misspelled) line after index 0.
	newLines := []string{"one", "wrongwordxyz", "two", "three", "four"}

	lineDecors, _ := s.diffAndCheck("notes.txt", oldLines, newLines, oldLineDecors, nil)

	if got := lineDecors[0]; len(got) != 1 || got[0].FixData != "poison-first" {
		t.Errorf("line 0 (unchanged) = %+v, want carried-forward poison marker", got)
	}
	// "four" was old line 3; after inserting one line before it, it must be
	// carried forward to new line 4 — and, crucially, still be the *cached*
	// (poisoned) entry, not a fresh (empty, since "four" is correctly
	// spelled) recompute.
	if got := lineDecors[4]; len(got) != 1 || got[0].FixData != "poison-last" {
		t.Errorf("line 4 (shifted from old line 3, unchanged) = %+v, want carried-forward poison marker", got)
	}
	// The genuinely new line must be freshly checked and flagged.
	got, ok := lineDecors[1]
	if !ok || len(got) == 0 {
		t.Fatalf("line 1 (newly inserted) has no decorations, want a flagged misspelling")
	}
	var payload fixPayload
	if err := json.Unmarshal([]byte(got[0].FixData), &payload); err != nil {
		t.Fatalf("unmarshal FixData: %v", err)
	}
	if payload.Word != "wrongwordxyz" {
		t.Errorf("line 1 flagged word = %q, want %q", payload.Word, "wrongwordxyz")
	}
	// Lines 2/3 ("two"/"three", shifted from old 1/2) were also unchanged
	// but had no cached entry, and are correctly spelled — they must stay
	// empty, not pick up a stray decoration.
	if got := lineDecors[2]; len(got) != 0 {
		t.Errorf("line 2 = %+v, want no decorations", got)
	}
	if got := lineDecors[3]; len(got) != 0 {
		t.Errorf("line 3 = %+v, want no decorations", got)
	}
}

// TestInvalidateAllClearsIncrementalBaseline is a regression test for a bug
// the incremental-diff cache would otherwise reintroduce: adding a word to
// the dictionary changes which words fail s.spell without changing any
// line's text, so a same-content diff against a stale baseline would find
// zero changed lines and carry every now-stale decoration straight through.
// invalidateAll must drop the diff baseline, not just the flat decoration
// cache, so the next check treats every line as changed.
func TestInvalidateAllClearsIncrementalBaseline(t *testing.T) {
	s := newTestSpellWithChecker(t)
	const bufID = 7
	s.bufPaths[bufID] = "notes.txt"
	s.lastLines[bufID] = []string{"stale line"}
	s.lineDecors[bufID] = map[uint32][]sdk.Decoration{0: {{FixData: "stale"}}}
	s.lineDiags[bufID] = map[uint32][]sdk.Diagnostic{0: {{Message: "stale"}}}

	s.invalidateAll()

	// invalidateAll's scheduleCheck fires checkBuffer (which needs a live
	// s.api, nil in this unit test) after checkDebounce — cancel the pending
	// timer immediately so it never fires in the background after this test
	// has already asserted and returned.
	s.mu.Lock()
	for _, timer := range s.pending {
		if timer != nil {
			timer.Stop()
		}
	}
	s.mu.Unlock()

	if got := s.lastLines[bufID]; len(got) != 0 {
		t.Errorf("lastLines[bufID] = %v, want cleared", got)
	}
	if got := s.lineDecors[bufID]; len(got) != 0 {
		t.Errorf("lineDecors[bufID] = %v, want cleared", got)
	}
	if got := s.lineDiags[bufID]; len(got) != 0 {
		t.Errorf("lineDiags[bufID] = %v, want cleared", got)
	}
}

func TestCheckDebounceIs250ms(t *testing.T) {
	if checkDebounce != 250*time.Millisecond {
		t.Errorf("checkDebounce = %v, want 250ms", checkDebounce)
	}
}
