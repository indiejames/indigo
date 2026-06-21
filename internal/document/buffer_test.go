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
