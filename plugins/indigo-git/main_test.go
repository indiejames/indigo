package main

import (
	"strings"
	"testing"
	"time"

	"github.com/indiejames/indigo/sdk"
)

func TestParseDiffHunkStarts(t *testing.T) {
	// Two hunks: a pure addition at line 3, and a pure deletion (no new
	// lines) whose newStart is 0 and must clamp to 1.
	diff := "@@ -1,0 +3,2 @@\n" +
		"+added one\n" +
		"+added two\n" +
		"@@ -1,2 +0,0 @@\n" +
		"-removed one\n" +
		"-removed two\n"

	detail := parseDiff(diff)
	lines, hunkStarts := detail.lines, detail.hunkStarts

	if lines[3] != lineAdded || lines[4] != lineAdded {
		t.Fatalf("expected lines 3,4 added, got %v", lines)
	}
	if lines[1] != lineDeleted {
		t.Fatalf("expected deletion marker clamped to line 1, got %v", lines)
	}
	want := []int{3, 1}
	if len(hunkStarts) != len(want) {
		t.Fatalf("hunkStarts = %v, want %v", hunkStarts, want)
	}
	for i, w := range want {
		if hunkStarts[i] != w {
			t.Fatalf("hunkStarts[%d] = %d, want %d", i, hunkStarts[i], w)
		}
	}
}

// TestParseDiffMixedHunk covers a hunk that both removes and adds lines.
//
// The fixture is real `diff --unified=0` output, which is what production
// runs: no context lines, so the header counts describe only the -/+ lines.
// (It previously carried a " context" line while claiming oldCount=2, which
// no --unified=0 run can produce.)
//
// Both new lines are ADDED, matching git: git has no "modified line" concept,
// only `-` and `+`. The removed line is reported separately as a removed row,
// so the pair renders red-then-green exactly as `git diff` shows it.
func TestParseDiffMixedHunk(t *testing.T) {
	diff := "@@ -5,1 +5,2 @@\n" +
		"-old\n" +
		"+new one\n" +
		"+new two\n"

	detail := parseDiff(diff)
	lines, hunkStarts := detail.lines, detail.hunkStarts
	for _, ln := range []int{5, 6} {
		if lines[ln] != lineAdded {
			t.Errorf("line %d = %v, want lineAdded", ln, lines[ln])
		}
	}
	if got := detail.removed[5]; len(got) != 1 || got[0].Text != "old" {
		t.Errorf("removed[5] = %+v, want the removed line reported separately", got)
	}
	if len(hunkStarts) != 1 || hunkStarts[0] != 5 {
		t.Fatalf("hunkStarts = %v, want [5]", hunkStarts)
	}
}

// TestParseDiffMatchesReportedHunk pins the exact diff from the bug report:
// harmony/test.ts, two lines removed and six added, which rendered as six
// blue lines when `git diff` showed two red and six green.
//
// Every added line is now lineAdded (green) and the two removed lines are
// reported as removed rows (red), so indigo's colours match the terminal's.
func TestParseDiffMatchesReportedHunk(t *testing.T) {
	diff := "@@ -1,2 +1,6 @@\n" +
		"-function foo() {\n" +
		"-  console.log(\"FOO\");\n" +
		"+function foo() {}\n" +
		"+\n" +
		"+function bar() {}\n" +
		"+\n" +
		"+function bazz() {\n" +
		"+  console.log(\"BAZZ\");\n"

	d := parseDiff(diff)
	for ln := 1; ln <= 6; ln++ {
		if d.lines[ln] != lineAdded {
			t.Errorf("line %d = %v, want lineAdded (git shows all six as additions)",
				ln, d.lines[ln])
		}
	}
	if got := d.removed[1]; len(got) != 2 {
		t.Fatalf("removed[1] = %+v, want the two removed lines", got)
	}
}

func TestJumpHunkForwardWraps(t *testing.T) {
	g := &GitPlugin{bufs: map[uint32]bufState{
		1: {hunkStarts: []int{5, 20, 40}},
	}}
	g.api = nil // onToggleBlame/etc paths not exercised by jumpHunk on the found branch

	resp := g.jumpHunk(fakeCtx(1, 0), true) // cursor before line 5 (0-indexed line 0 -> 1-indexed 1)
	if !resp.HasCursor || resp.CursorLine != 4 {
		t.Fatalf("expected cursor line 4 (1-indexed 5), got %+v", resp)
	}

	resp = g.jumpHunk(fakeCtx(1, 39), true) // cursor at last hunk (1-indexed 40) -> wraps to first
	if !resp.HasCursor || resp.CursorLine != 4 {
		t.Fatalf("expected wrap to first hunk (cursor line 4), got %+v", resp)
	}
}

func TestJumpHunkBackwardWraps(t *testing.T) {
	g := &GitPlugin{bufs: map[uint32]bufState{
		1: {hunkStarts: []int{5, 20, 40}},
	}}

	resp := g.jumpHunk(fakeCtx(1, 39), false) // 1-indexed 40 -> previous is 20
	if !resp.HasCursor || resp.CursorLine != 19 {
		t.Fatalf("expected cursor line 19 (1-indexed 20), got %+v", resp)
	}

	resp = g.jumpHunk(fakeCtx(1, 0), false) // 1-indexed 1, before first hunk -> wraps to last
	if !resp.HasCursor || resp.CursorLine != 39 {
		t.Fatalf("expected wrap to last hunk (cursor line 39), got %+v", resp)
	}
}

func TestParseBlamePorcelainFirstAndRepeatOccurrence(t *testing.T) {
	out := "abcdef0123456789abcdef0123456789abcdef01 1 1 2\n" +
		"author Jane Doe\n" +
		"author-time 1700000000\n" +
		"summary Fix the thing\n" +
		"filename foo.go\n" +
		"\tline one\n" +
		"abcdef0123456789abcdef0123456789abcdef01 2 2\n" +
		"\tline two\n" +
		"0000000000000000000000000000000000000000 3 3 1\n" +
		"author Not Committed Yet\n" +
		"author-time 1700000001\n" +
		"summary Not Committed Yet\n" +
		"filename foo.go\n" +
		"\tline three\n"

	blame := parseBlamePorcelain(out)

	bl1, ok := blame[1]
	if !ok || bl1.author != "Jane Doe" || bl1.summary != "Fix the thing" {
		t.Fatalf("line 1 = %+v", bl1)
	}
	if bl1.when.Unix() != 1700000000 {
		t.Fatalf("line 1 when = %v", bl1.when)
	}

	// Repeat occurrence of the same hash carries no metadata lines of its
	// own — it must reuse the metadata captured on first occurrence.
	bl2, ok := blame[2]
	if !ok || bl2.author != "Jane Doe" || bl2.hash != bl1.hash {
		t.Fatalf("line 2 = %+v, want reused metadata from line 1", bl2)
	}

	bl3, ok := blame[3]
	if !ok || !isZeroHash(bl3.hash) {
		t.Fatalf("line 3 = %+v, want zero-hash uncommitted entry", bl3)
	}
}

func TestParseBlamePorcelainSHA256Hash(t *testing.T) {
	// git's experimental SHA-256 object format uses 64-hex-char names instead
	// of SHA-1's 40; the header regex must accept both.
	hash64 := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab"[:64]
	out := hash64 + " 1 1 1\n" +
		"author Jane Doe\n" +
		"author-time 1700000000\n" +
		"summary Fix the thing\n" +
		"filename foo.go\n" +
		"\tline one\n"

	blame := parseBlamePorcelain(out)
	bl, ok := blame[1]
	if !ok || bl.hash != hash64 || bl.author != "Jane Doe" {
		t.Fatalf("line 1 = %+v, ok=%v, want parsed SHA-256 blame entry", bl, ok)
	}
}

func TestIsZeroHash(t *testing.T) {
	if !isZeroHash("0000000000000000000000000000000000000000") {
		t.Fatal("expected all-zero hash to be recognized")
	}
	if isZeroHash("abcdef0123456789abcdef0123456789abcdef01") {
		t.Fatal("did not expect a real hash to be recognized as zero")
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		when time.Time
		want string
	}{
		{now.Add(-10 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5 minutes ago"},
		{now.Add(-1 * time.Hour), "1 hour ago"},
		{now.Add(-3 * 24 * time.Hour), "3 days ago"},
	}
	for _, c := range cases {
		if got := relativeTime(c.when); got != c.want {
			t.Errorf("relativeTime(%v) = %q, want %q", c.when, got, c.want)
		}
	}
}

func TestFormatBlameOverlayUncommitted(t *testing.T) {
	bl := blameLine{hash: "0000000000000000000000000000000000000000"}
	if got := formatBlameOverlay(bl); got != "  uncommitted" {
		t.Fatalf("formatBlameOverlay = %q, want uncommitted marker", got)
	}
}

// fakeCtx builds a minimal normal-mode KeyContext for the given buffer/cursor line.
func fakeCtx(bufID uint32, cursorLine uint32) sdk.KeyContext {
	return sdk.KeyContext{BufID: bufID, Mode: "normal", CursorLine: cursorLine}
}

// TestParseDiffCapturesRemovedText covers what the parser previously threw
// away: with --unified=0 the `-` lines are right there in the output, but
// only the @@ headers were ever read, so removed content could not be shown
// in place.
func TestParseDiffCapturesRemovedText(t *testing.T) {
	diff := "--- a\n+++ -\n" +
		"@@ -5,2 +5,2 @@\n" +
		"-return err\n" +
		"-old second\n" +
		"+return nil\n" +
		"+new second\n"

	d := parseDiff(diff)

	got := d.removed[5]
	if len(got) != 2 {
		t.Fatalf("removed[5] has %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Text != "return err" || got[1].Text != "old second" {
		t.Errorf("removed text = %q / %q, want %q / %q",
			got[0].Text, got[1].Text, "return err", "old second")
	}
	// The file headers must not be mistaken for content lines.
	for _, rl := range got {
		if strings.HasPrefix(rl.Text, "-") || strings.HasPrefix(rl.Text, "+") {
			t.Errorf("removed entry %q looks like an unstripped diff marker", rl.Text)
		}
	}
}

// TestParseDiffPureDeletionHasNoEmphasis pins that a deleted line with no
// replacement carries no intra-line range — there is nothing to compare it
// against, and emphasising all of it would be noise.
func TestParseDiffPureDeletionHasNoEmphasis(t *testing.T) {
	// "+4,0" = zero lines at new-file position 4, so the removed content sat
	// after line 4 and anchors above line 5. (This expectation originally
	// said 4, encoding the off-by-one that
	// TestParseDiffAnchorsRemovedLinesAtTheDeletionPoint later caught.)
	d := parseDiff("@@ -5,1 +4,0 @@\n-gone entirely\n")

	got := d.removed[5]
	if len(got) != 1 || got[0].Text != "gone entirely" {
		t.Fatalf("removed[5] = %+v (whole map %+v), want the deleted line", got, d.removed)
	}
	if got[0].EmphStart != got[0].EmphEnd {
		t.Errorf("pure deletion carries emphasis [%d,%d), want an empty range",
			got[0].EmphStart, got[0].EmphEnd)
	}
	if len(d.emph) != 0 {
		t.Errorf("pure deletion produced buffer-side emphasis %v, want none", d.emph)
	}
}

func TestIntraLineDiff(t *testing.T) {
	cases := []struct {
		name             string
		old, new         string
		wantOld, wantNew [2]int
	}{
		{"change in the middle", "return err", "return nil", [2]int{7, 10}, [2]int{7, 10}},
		{"identical", "same", "same", [2]int{4, 4}, [2]int{4, 4}},
		{"appended", "abc", "abcdef", [2]int{3, 3}, [2]int{3, 6}},
		{"truncated", "abcdef", "abc", [2]int{3, 6}, [2]int{3, 3}},
		{"whole line differs", "aaa", "bbb", [2]int{0, 3}, [2]int{0, 3}},
		{"empty old", "", "new", [2]int{0, 0}, [2]int{0, 3}},
		// Repeated runs: prefix and suffix must not overlap. "aaa"->"aa"
		// leaves exactly one deleted rune to emphasise on the old side and
		// nothing on the new; an overlapping scan would produce a negative
		// or double-counted range.
		{"repeated runs", "aaa", "aa", [2]int{2, 3}, [2]int{2, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOld, gotNew := intraLineDiff(tc.old, tc.new)
			if gotOld != tc.wantOld || gotNew != tc.wantNew {
				t.Errorf("intraLineDiff(%q,%q) = %v,%v want %v,%v",
					tc.old, tc.new, gotOld, gotNew, tc.wantOld, tc.wantNew)
			}
		})
	}
}

// TestIntraLineDiffIsRuneSafe guards against byte-vs-rune confusion: the
// ranges are consumed as rune columns by the renderer, so a multi-byte
// character before the change must not shift them.
func TestIntraLineDiffIsRuneSafe(t *testing.T) {
	oldR, newR := intraLineDiff("héllo wörld", "héllo wörle")
	if oldR != [2]int{10, 11} || newR != [2]int{10, 11} {
		t.Errorf("got %v,%v want [10 11],[10 11] — ranges must be in runes, not bytes", oldR, newR)
	}
}

// TestParseDiffAnchorsRemovedLinesAtTheDeletionPoint is a regression test for
// a reported bug: deleting the second line of a file showed the removed text
// above line 1 instead of between lines 1 and 2.
//
// The cause is a diff-format subtlety. For a pure deletion, "@@ -2 +1,0 @@"
// means zero lines at new-file position 1 — the removed content sat *after*
// new line 1, so it anchors above line 2. That differs from a mixed hunk,
// where newStart is the replacement line and the removed content does belong
// directly above it.
func TestParseDiffAnchorsRemovedLinesAtTheDeletionPoint(t *testing.T) {
	cases := []struct {
		name       string
		diff       string
		wantAnchor int
		wantText   string
	}{
		{
			// The reported case: 3-line file, middle line deleted.
			name:       "deletion after the first line",
			diff:       "@@ -2 +1,0 @@\n-  console.log(\"FOO\");\n",
			wantAnchor: 2, // above new line 2, i.e. between line 1 and line 2
			wantText:   `  console.log("FOO");`,
		},
		{
			name:       "deletion at the very start of the file",
			diff:       "@@ -1 +0,0 @@\n-first\n",
			wantAnchor: 1, // above what is now line 1
			wantText:   "first",
		},
		{
			// A mixed hunk keeps anchoring on the replacement line.
			name:       "changed line",
			diff:       "@@ -5,1 +5,1 @@\n-return err\n+return nil\n",
			wantAnchor: 5,
			wantText:   "return err",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := parseDiff(tc.diff)
			got, ok := d.removed[tc.wantAnchor]
			if !ok || len(got) != 1 {
				t.Fatalf("removed[%d] = %+v (whole map %+v), want one entry",
					tc.wantAnchor, got, d.removed)
			}
			if got[0].Text != tc.wantText {
				t.Errorf("removed text = %q, want %q", got[0].Text, tc.wantText)
			}
		})
	}
}

// TestParseDiffDeletionAtEndOfFile documents where the removed row lands when
// the deletion is past the last remaining line. A file ending in a newline has
// a phantom trailing line in the buffer, so the anchor is still in range and
// the removed text renders below the last real line. Without that phantom
// line the anchor is out of range and the client drops it — a known gap.
func TestParseDiffDeletionAtEndOfFile(t *testing.T) {
	// 2-line file, last line deleted: "@@ -2 +1,0 @@".
	d := parseDiff("@@ -2 +1,0 @@\n-last\n")

	got := d.removed[2]
	if len(got) != 1 || got[0].Text != "last" {
		t.Fatalf("removed[2] = %+v, want the deleted last line", got)
	}
}
