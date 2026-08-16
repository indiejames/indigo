package document

import (
	"strings"
	"testing"
)

func TestNewEmpty(t *testing.T) {
	b := New("", "")
	if b.LineCount() != 1 {
		t.Fatalf("empty: LineCount=%d, want 1", b.LineCount())
	}
	if b.Line(0) != "" {
		t.Fatalf("empty: Line(0)=%q, want %q", b.Line(0), "")
	}
	if b.Content() != "" {
		t.Fatalf("empty: Content()=%q, want %q", b.Content(), "")
	}
}

func TestNewContent(t *testing.T) {
	b := New("", "hello\nworld")
	if b.LineCount() != 2 {
		t.Fatalf("LineCount=%d, want 2", b.LineCount())
	}
	if b.Line(0) != "hello" {
		t.Fatalf("Line(0)=%q, want hello", b.Line(0))
	}
	if b.Line(1) != "world" {
		t.Fatalf("Line(1)=%q, want world", b.Line(1))
	}
}

func TestTrailingNewline(t *testing.T) {
	b := New("", "foo\n")
	if b.LineCount() != 2 {
		t.Fatalf("trailing-nl: LineCount=%d, want 2", b.LineCount())
	}
	if b.Line(1) != "" {
		t.Fatalf("trailing-nl: Line(1)=%q, want empty", b.Line(1))
	}
	if b.Content() != "foo\n" {
		t.Fatalf("trailing-nl: Content()=%q", b.Content())
	}
}

func TestInsertChar(t *testing.T) {
	b := New("", "hello")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 5, InsertText: " world"})
	if b.Content() != "hello world" {
		t.Fatalf("insert: Content=%q", b.Content())
	}
}

func TestInsertNewline(t *testing.T) {
	b := New("", "helloworld")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "\n"})
	if b.LineCount() != 2 {
		t.Fatalf("insert-nl: LineCount=%d", b.LineCount())
	}
	if b.Line(0) != "hello" || b.Line(1) != "world" {
		t.Fatalf("insert-nl: Line(0)=%q Line(1)=%q", b.Line(0), b.Line(1))
	}
}

func TestDeleteChar(t *testing.T) {
	b := New("", "hello world")
	b.Apply(Op{Type: OpDelete, FromLine: 0, FromCol: 5, ToLine: 0, ToCol: 11})
	if b.Content() != "hello" {
		t.Fatalf("delete: Content=%q", b.Content())
	}
}

func TestDeleteAcrossLines(t *testing.T) {
	b := New("", "hello\nworld")
	b.Apply(Op{Type: OpDelete, FromLine: 0, FromCol: 3, ToLine: 1, ToCol: 3})
	if b.Content() != "helld" {
		t.Fatalf("delete-across: Content=%q", b.Content())
	}
}

func TestMultipleInserts(t *testing.T) {
	b := New("", "")
	for _, ch := range "hello world" {
		col := b.LineLen(0)
		b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: col, InsertText: string(ch)})
	}
	if b.Content() != "hello world" {
		t.Fatalf("multi-insert: Content=%q", b.Content())
	}
}

func TestInsertAtBeginning(t *testing.T) {
	b := New("", "world")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "hello "})
	if b.Content() != "hello world" {
		t.Fatalf("insert-begin: Content=%q", b.Content())
	}
}

func TestRuneOffset(t *testing.T) {
	b := New("", "hello\nworld")
	if off := b.RuneOffset(1, 0); off != 6 {
		t.Fatalf("RuneOffset(1,0)=%d, want 6", off)
	}
	if off := b.RuneOffset(1, 3); off != 9 {
		t.Fatalf("RuneOffset(1,3)=%d, want 9", off)
	}
}

func TestPosFromOffset(t *testing.T) {
	b := New("", "hello\nworld")
	p := b.PosFromOffset(6)
	if p.Line != 1 || p.Col != 0 {
		t.Fatalf("PosFromOffset(6)=%v, want {1,0}", p)
	}
	p = b.PosFromOffset(9)
	if p.Line != 1 || p.Col != 3 {
		t.Fatalf("PosFromOffset(9)=%v, want {1,3}", p)
	}
}

// TestPosFromOffsetTrailingNewlinePhantomLine covers the documented
// off-by-one hotspot: a trailing "\n" produces a phantom empty final line
// (LineCount counts it), and an offset at or past end-of-content must land
// on that phantom line rather than the last real line of text.
func TestPosFromOffsetTrailingNewlinePhantomLine(t *testing.T) {
	b := New("", "hello\nworld\n")
	if got := b.LineCount(); got != 3 {
		t.Fatalf("LineCount()=%d, want 3 (phantom trailing line)", got)
	}

	// Offset at/past total length must resolve to the phantom line {2, 0},
	// not to column-past-end of the "world" line.
	total := len("hello\nworld\n")
	p := b.PosFromOffset(total)
	if p.Line != 2 || p.Col != 0 {
		t.Fatalf("PosFromOffset(total=%d)=%v, want {2,0} (phantom line)", total, p)
	}
	p = b.PosFromOffset(total + 5) // past end entirely — should clamp, not overshoot the phantom line
	if p.Line != 2 || p.Col != 0 {
		t.Fatalf("PosFromOffset(past end)=%v, want {2,0} (phantom line)", p)
	}

	// Offset immediately before the trailing newline still resolves within
	// the "world" line, confirming the phantom line only kicks in once the
	// trailing newline itself has been consumed.
	p = b.PosFromOffset(total - 1)
	if p.Line != 1 || p.Col != 5 {
		t.Fatalf("PosFromOffset(total-1)=%v, want {1,5} (end of \"world\")", p)
	}
}

func TestLargeFile(t *testing.T) {
	var sb strings.Builder
	for range 1000 {
		sb.WriteString("line content here\n")
	}
	content := sb.String()
	b := New("", content)
	if b.LineCount() != 1001 {
		t.Fatalf("large: LineCount=%d, want 1001", b.LineCount())
	}
	if b.Line(500) != "line content here" {
		t.Fatalf("large: Line(500)=%q", b.Line(500))
	}
	if b.Content() != content {
		t.Fatal("large: Content() mismatch")
	}
}

func TestOpsSince(t *testing.T) {
	b := New("", "hello")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 5, InsertText: " world"})
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 11, InsertText: "!"})
	ops := b.OpsSince(0)
	if len(ops) != 2 {
		t.Fatalf("OpsSince(0): got %d ops, want 2", len(ops))
	}
	ops = b.OpsSince(1)
	if len(ops) != 1 {
		t.Fatalf("OpsSince(1): got %d ops, want 1", len(ops))
	}
}

func TestTrimHistory(t *testing.T) {
	b := New("", "")
	for i := 0; i < 5; i++ {
		b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: i, InsertText: "x"})
	}
	if got := b.HistoryLen(); got != 5 {
		t.Fatalf("HistoryLen before trim: got %d, want 5", got)
	}

	b.TrimHistory(3)
	if got := b.HistoryLen(); got != 2 {
		t.Fatalf("HistoryLen after TrimHistory(3): got %d, want 2", got)
	}

	// Ops at/before the trimmed version are gone; ops after it are unaffected.
	ops := b.OpsSince(3)
	if len(ops) != 2 {
		t.Fatalf("OpsSince(3) after trim: got %d ops, want 2", len(ops))
	}
	if ops[0].Version != 4 || ops[1].Version != 5 {
		t.Fatalf("OpsSince(3) after trim: got versions %d,%d, want 4,5", ops[0].Version, ops[1].Version)
	}

	// A request older than the trim point clamps to what's still retained
	// instead of panicking or returning garbage.
	ops = b.OpsSince(0)
	if len(ops) != 2 {
		t.Fatalf("OpsSince(0) after trim: got %d ops, want 2 (clamped)", len(ops))
	}

	// Further edits keep appending correctly after a trim.
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "y"})
	if got := b.HistoryLen(); got != 3 {
		t.Fatalf("HistoryLen after post-trim edit: got %d, want 3", got)
	}
	ops = b.OpsSince(3)
	if len(ops) != 3 {
		t.Fatalf("OpsSince(3) after post-trim edit: got %d ops, want 3", len(ops))
	}

	// Trimming at or before the current base is a no-op.
	b.TrimHistory(3)
	if got := b.HistoryLen(); got != 3 {
		t.Fatalf("HistoryLen after redundant TrimHistory(3): got %d, want 3", got)
	}

	// Trimming past the current version retains nothing but doesn't panic.
	b.TrimHistory(1000)
	if got := b.HistoryLen(); got != 0 {
		t.Fatalf("HistoryLen after over-trim: got %d, want 0", got)
	}
	if ops := b.OpsSince(0); len(ops) != 0 {
		t.Fatalf("OpsSince(0) after over-trim: got %d ops, want 0", len(ops))
	}
}

// TestTrimHistoryOverTrimThenReclaim is a regression test: TrimHistory(N)
// for an N beyond the last retained op's version must set historyBase to
// the actual number of ops removed, not the raw requested N. Setting it to
// N directly left historyBase pointing past the buffer's real version;
// OpsSince's defensive "sinceVersion < historyBase" clamp then silently
// rewound any later sinceVersion down to that inflated historyBase,
// re-returning ops the caller had already seen.
func TestTrimHistoryOverTrimThenReclaim(t *testing.T) {
	b := New("", "")
	for i := 0; i < 3; i++ {
		b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: i, InsertText: "x"})
	}
	// Over-trim: request far beyond the current version (3); this must
	// clamp internally rather than adopting 1000 as the new historyBase.
	b.TrimHistory(1000)
	if got := b.HistoryLen(); got != 0 {
		t.Fatalf("HistoryLen after over-trim: got %d, want 0", got)
	}

	for i := 0; i < 3; i++ {
		b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "y"})
	}
	// Buffer is now at version 6 with 3 ops (versions 4,5,6) retained.
	if got := b.HistoryLen(); got != 3 {
		t.Fatalf("HistoryLen after post-overtrim edits: got %d, want 3", got)
	}

	// A caller already caught up to version 4 must get exactly the 2 ops
	// after it, not the already-seen version-4 op re-included.
	ops := b.OpsSince(4)
	if len(ops) != 2 {
		t.Fatalf("OpsSince(4) after over-trim: got %d ops, want 2", len(ops))
	}
	if ops[0].Version != 5 || ops[1].Version != 6 {
		t.Fatalf("OpsSince(4) after over-trim: got versions %d,%d, want 5,6", ops[0].Version, ops[1].Version)
	}

	// A subsequent legitimate trim (once a caller catches up to 6) must
	// still work correctly off the corrected historyBase.
	b.TrimHistory(6)
	if got := b.HistoryLen(); got != 0 {
		t.Fatalf("HistoryLen after second trim: got %d, want 0", got)
	}
}

// TestOpsSinceAndVersionAtomicUnderConcurrentApply is a regression test for
// a race in the server's GetUpdates handler: it used to call OpsSince and
// Version as two separate locked reads, so a concurrent Apply (as happens
// when a plugin edits a buffer directly, bypassing the RPC call queue) could
// land between them and make the reported version outrun the ops actually
// returned. A polling client trusts the reported version as "I've now seen
// everything up to here," so the skipped op would never be redelivered —
// permanent, silent desync. OpsSinceAndVersion reads both under one lock so
// this can't happen; this test hammers Apply concurrently with
// OpsSinceAndVersion and asserts the invariant that must hold either way.
func TestOpsSinceAndVersionAtomicUnderConcurrentApply(t *testing.T) {
	b := New("", "")
	const n = 3000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < n; i++ {
			b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
		}
	}()

	for i := 0; i < n; i++ {
		ops, ver := b.OpsSinceAndVersion(0)
		if len(ops) == 0 {
			continue // Apply goroutine hasn't landed its first op yet
		}
		if got := ops[len(ops)-1].Version; got != ver {
			t.Fatalf("OpsSinceAndVersion: last returned op version=%d, reported version=%d — must always match under a concurrent Apply", got, ver)
		}
	}
	<-done
}

func TestDirty(t *testing.T) {
	b := New("", "hello")
	if b.Dirty() {
		t.Fatal("new buffer should not be dirty")
	}
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "x"})
	if !b.Dirty() {
		t.Fatal("buffer should be dirty after apply")
	}
	b.SetClean()
	if b.Dirty() {
		t.Fatal("buffer should not be dirty after SetClean")
	}
}

func TestGapFlushOnFarInsert(t *testing.T) {
	// Insert at position 0, then insert far away — forces a gap flush.
	b := New("", "abcdefghij")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "X"})
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 11, InsertText: "Y"})
	want := "XabcdefghijY"
	if b.Content() != want {
		t.Fatalf("far-insert: Content=%q, want %q", b.Content(), want)
	}
}

func TestDeleteInGap(t *testing.T) {
	// Insert several chars then delete some — all within gap.
	b := New("", "")
	b.Apply(Op{Type: OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "hello"})
	b.Apply(Op{Type: OpDelete, FromLine: 0, FromCol: 1, ToLine: 0, ToCol: 3})
	if b.Content() != "hlo" {
		t.Fatalf("delete-in-gap: Content=%q, want hlo", b.Content())
	}
}

func TestLineLen(t *testing.T) {
	b := New("", "hello\nworld\n")
	if b.LineLen(0) != 5 {
		t.Fatalf("LineLen(0)=%d, want 5", b.LineLen(0))
	}
	if b.LineLen(1) != 5 {
		t.Fatalf("LineLen(1)=%d, want 5", b.LineLen(1))
	}
	if b.LineLen(2) != 0 {
		t.Fatalf("LineLen(2)=%d, want 0", b.LineLen(2))
	}
}
