package client

import (
	"testing"

	"github.com/indiejames/twist/internal/document"
)

func TestInsertEndPos(t *testing.T) {
	tests := []struct {
		fromLine, fromCol int
		text              string
		wantLine, wantCol int
	}{
		{0, 0, "hello", 0, 5},
		{0, 0, "", 0, 0},
		{0, 5, " world", 0, 11},
		{0, 0, "\n", 1, 0},
		{0, 0, "hello\nworld", 1, 5},
		{0, 0, "a\nb\nc", 2, 1},
		{2, 3, "abc\ndef\n", 4, 0},
		{1, 0, "\n\n\n", 4, 0},
	}
	for _, tt := range tests {
		gotLine, gotCol := insertEndPos(tt.fromLine, tt.fromCol, tt.text)
		if gotLine != tt.wantLine || gotCol != tt.wantCol {
			t.Errorf("insertEndPos(%d, %d, %q) = (%d, %d), want (%d, %d)",
				tt.fromLine, tt.fromCol, tt.text,
				gotLine, gotCol, tt.wantLine, tt.wantCol)
		}
	}
}

func TestBufText(t *testing.T) {
	m := Model{buf: document.New("", "hello\nworld\nfoo")}

	tests := []struct {
		fromLine, fromCol, toLine, toCol int
		want                             string
	}{
		{0, 0, 0, 5, "hello"},
		{0, 1, 0, 4, "ell"},
		{1, 0, 1, 5, "world"},
		{0, 3, 1, 2, "lo\nwo"},
		{0, 0, 2, 3, "hello\nworld\nfoo"},
		{0, 5, 1, 0, "\n"}, // just the newline
	}
	for _, tt := range tests {
		got := bufText(m, tt.fromLine, tt.fromCol, tt.toLine, tt.toCol)
		if got != tt.want {
			t.Errorf("bufText(%d,%d,%d,%d) = %q, want %q",
				tt.fromLine, tt.fromCol, tt.toLine, tt.toCol, got, tt.want)
		}
	}
}

func TestInverseOpInsert(t *testing.T) {
	m := Model{buf: document.New("", "hello world")}
	op := document.Op{
		Type:       document.OpInsert,
		InsertLine: 0,
		InsertCol:  5,
		InsertText: " there",
	}
	inv := inverseOp(m, op)
	if inv.Type != document.OpDelete {
		t.Fatalf("inverse of insert should be delete, got %v", inv.Type)
	}
	if inv.FromLine != 0 || inv.FromCol != 5 {
		t.Errorf("inv.From = (%d,%d), want (0,5)", inv.FromLine, inv.FromCol)
	}
	if inv.ToLine != 0 || inv.ToCol != 11 {
		t.Errorf("inv.To = (%d,%d), want (0,11)", inv.ToLine, inv.ToCol)
	}
}

func TestInverseOpDelete(t *testing.T) {
	m := Model{buf: document.New("", "hello world")}
	op := document.Op{
		Type:     document.OpDelete,
		FromLine: 0, FromCol: 5,
		ToLine: 0, ToCol: 11,
	}
	inv := inverseOp(m, op)
	if inv.Type != document.OpInsert {
		t.Fatalf("inverse of delete should be insert, got %v", inv.Type)
	}
	if inv.InsertLine != 0 || inv.InsertCol != 5 {
		t.Errorf("inv.Insert = (%d,%d), want (0,5)", inv.InsertLine, inv.InsertCol)
	}
	if inv.InsertText != " world" {
		t.Errorf("inv.InsertText = %q, want %q", inv.InsertText, " world")
	}
}
