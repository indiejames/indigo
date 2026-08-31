package main

import (
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

	lines, hunkStarts := parseDiff(diff)

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

func TestParseDiffMixedHunk(t *testing.T) {
	diff := "@@ -5,2 +5,3 @@\n" +
		" context\n" +
		"-old\n" +
		"+new one\n" +
		"+new two\n"

	lines, hunkStarts := parseDiff(diff)
	for _, ln := range []int{5, 6, 7} {
		if lines[ln] != lineChanged {
			t.Fatalf("line %d = %v, want lineChanged", ln, lines[ln])
		}
	}
	if len(hunkStarts) != 1 || hunkStarts[0] != 5 {
		t.Fatalf("hunkStarts = %v, want [5]", hunkStarts)
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
