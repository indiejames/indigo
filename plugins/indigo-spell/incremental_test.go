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

// poisonDecor builds a decoration carrying a real (JSON) FixData payload, so
// tests can verify that a carried-forward, line-shifted decoration has both
// its own Line field *and* its embedded fixPayload.Line rewritten to the new
// line — moving the map key alone isn't enough, since GetDecorations
// publishes decorations flattened, keyed by nothing but their own Line.
func poisonDecor(t *testing.T, word string, line uint32) sdk.Decoration {
	t.Helper()
	b, err := json.Marshal(fixPayload{Word: word, Line: line, Col: 0})
	if err != nil {
		t.Fatalf("marshal fixPayload: %v", err)
	}
	return sdk.Decoration{Line: line, Col: 0, EndCol: uint32(len(word)), FixData: string(b)}
}

func poisonDiag(word string, line uint32) sdk.Diagnostic {
	return sdk.Diagnostic{
		Range: sdk.Range{
			Start: sdk.Position{Line: line, Col: 0},
			End:   sdk.Position{Line: line, Col: uint32(len(word))},
		},
		Message: "poison: " + word,
	}
}

// decorPayload unmarshals a decoration's FixData and fails the test if it's
// not valid JSON matching fixPayload — used to confirm a rebased decoration's
// embedded line number, not just its top-level Line field.
func decorPayload(t *testing.T, d sdk.Decoration) fixPayload {
	t.Helper()
	var payload fixPayload
	if err := json.Unmarshal([]byte(d.FixData), &payload); err != nil {
		t.Fatalf("unmarshal FixData %q: %v", d.FixData, err)
	}
	return payload
}

// TestDiffAndCheckShiftsCarriedLinesOnInsertedLine verifies that when an
// edit inserts a line in the middle, unchanged lines before and after the
// insertion point are both carried forward from cache (not recomputed —
// proven via a poison marker) and that the lines after the insertion point
// are correctly renumbered: not just the result map's key, but the carried
// decoration/diagnostic's own embedded line fields (Decoration.Line,
// fixPayload.Line inside FixData, and Diagnostic.Range.Start/End.Line).
func TestDiffAndCheckShiftsCarriedLinesOnInsertedLine(t *testing.T) {
	s := newTestSpellWithChecker(t)

	oldLines := []string{"one", "two", "three", "four"}
	oldLineDecors := map[uint32][]sdk.Decoration{
		0: {poisonDecor(t, "first", 0)},
		3: {poisonDecor(t, "last", 3)}, // attached to "four"
	}
	oldLineDiags := map[uint32][]sdk.Diagnostic{
		0: {poisonDiag("first", 0)},
		3: {poisonDiag("last", 3)},
	}

	// Insert a new (misspelled) line after index 0.
	newLines := []string{"one", "wrongwordxyz", "two", "three", "four"}

	lineDecors, lineDiags := s.diffAndCheck("notes.txt", oldLines, newLines, oldLineDecors, oldLineDiags)

	// Line 0 (prefix, unshifted) must be untouched.
	if got := lineDecors[0]; len(got) != 1 || got[0].Line != 0 || decorPayload(t, got[0]).Line != 0 {
		t.Errorf("line 0 (unchanged) = %+v, want carried-forward poison marker at line 0", got)
	}

	// "four" was old line 3; after inserting one line before it, it must be
	// carried forward to new line 4 with every embedded line reference
	// (Decoration.Line, FixData's fixPayload.Line, and the diagnostic's
	// Range) rewritten to 4, not left at the stale value 3.
	gotDecors, ok := lineDecors[4]
	if !ok || len(gotDecors) != 1 {
		t.Fatalf("line 4 (shifted from old line 3) decors = %+v, want one carried-forward entry", gotDecors)
	}
	if gotDecors[0].Line != 4 {
		t.Errorf("line 4 decoration.Line = %d, want 4 (rebased, not stale 3)", gotDecors[0].Line)
	}
	if payload := decorPayload(t, gotDecors[0]); payload.Word != "last" || payload.Line != 4 {
		t.Errorf("line 4 FixData payload = %+v, want {Word: last, Line: 4}", payload)
	}
	gotDiags, ok := lineDiags[4]
	if !ok || len(gotDiags) != 1 {
		t.Fatalf("line 4 diags = %+v, want one carried-forward entry", gotDiags)
	}
	if gotDiags[0].Range.Start.Line != 4 || gotDiags[0].Range.End.Line != 4 {
		t.Errorf("line 4 diagnostic.Range = %+v, want Start/End.Line = 4 (rebased, not stale 3)", gotDiags[0].Range)
	}

	// The genuinely new line must be freshly checked and flagged, at its own
	// (correct, freshly computed) line number.
	got, ok := lineDecors[1]
	if !ok || len(got) == 0 {
		t.Fatalf("line 1 (newly inserted) has no decorations, want a flagged misspelling")
	}
	if got[0].Line != 1 {
		t.Errorf("line 1 decoration.Line = %d, want 1", got[0].Line)
	}
	if payload := decorPayload(t, got[0]); payload.Word != "wrongwordxyz" {
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

// TestDiffAndCheckShiftsCarriedLinesOnDeletedLine is the deletion-side
// counterpart: removing a line in the middle must both drop that line's
// stale cached decoration entirely (rather than leaking it at some other
// line) and shift every line after it down, with each carried entry's
// embedded line fields rebased to the new (lower) line number.
func TestDiffAndCheckShiftsCarriedLinesOnDeletedLine(t *testing.T) {
	s := newTestSpellWithChecker(t)

	oldLines := []string{"one", "two", "deletemexyz", "three", "four"}
	oldLineDecors := map[uint32][]sdk.Decoration{
		0: {poisonDecor(t, "first", 0)},
		2: {poisonDecor(t, "deletemexyz", 2)}, // the line about to be deleted
		3: {poisonDecor(t, "three-marker", 3)},
		4: {poisonDecor(t, "four-marker", 4)},
	}
	oldLineDiags := map[uint32][]sdk.Diagnostic{
		3: {poisonDiag("three-marker", 3)},
		4: {poisonDiag("four-marker", 4)},
	}

	// Delete old line 2 ("deletemexyz").
	newLines := []string{"one", "two", "three", "four"}

	lineDecors, lineDiags := s.diffAndCheck("notes.txt", oldLines, newLines, oldLineDecors, oldLineDiags)

	// Line 0 (prefix, unshifted) must be untouched.
	if got := lineDecors[0]; len(got) != 1 || got[0].Line != 0 {
		t.Errorf("line 0 (unchanged) = %+v, want carried-forward poison marker at line 0", got)
	}

	// old line 3 ("three") shifts down to new line 2.
	gotDecors, ok := lineDecors[2]
	if !ok || len(gotDecors) != 1 {
		t.Fatalf("line 2 (shifted from old line 3) decors = %+v, want one carried-forward entry", gotDecors)
	}
	if gotDecors[0].Line != 2 {
		t.Errorf("line 2 decoration.Line = %d, want 2 (rebased, not stale 3)", gotDecors[0].Line)
	}
	if payload := decorPayload(t, gotDecors[0]); payload.Word != "three-marker" || payload.Line != 2 {
		t.Errorf("line 2 FixData payload = %+v, want {Word: three-marker, Line: 2}", payload)
	}
	gotDiags, ok := lineDiags[2]
	if !ok || len(gotDiags) != 1 || gotDiags[0].Range.Start.Line != 2 || gotDiags[0].Range.End.Line != 2 {
		t.Errorf("line 2 diags = %+v, want one entry with Start/End.Line = 2", gotDiags)
	}

	// old line 4 ("four") shifts down to new line 3.
	gotDecors, ok = lineDecors[3]
	if !ok || len(gotDecors) != 1 {
		t.Fatalf("line 3 (shifted from old line 4) decors = %+v, want one carried-forward entry", gotDecors)
	}
	if gotDecors[0].Line != 3 {
		t.Errorf("line 3 decoration.Line = %d, want 3 (rebased, not stale 4)", gotDecors[0].Line)
	}
	if payload := decorPayload(t, gotDecors[0]); payload.Word != "four-marker" || payload.Line != 3 {
		t.Errorf("line 3 FixData payload = %+v, want {Word: four-marker, Line: 3}", payload)
	}

	// The deleted line's own stale decoration must not leak anywhere in the
	// result (not at key 2, not at any other key).
	for line, decors := range lineDecors {
		for _, d := range decors {
			if payload := decorPayload(t, d); payload.Word == "deletemexyz" {
				t.Errorf("deleted line's decoration leaked into result at line %d: %+v", line, d)
			}
		}
	}

	// Only 4 lines exist now; nothing should be keyed at old indices beyond
	// that.
	if _, ok := lineDecors[4]; ok {
		t.Errorf("lineDecors[4] present, want no entry (file only has 4 lines now)")
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
