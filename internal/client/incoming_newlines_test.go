package client

import (
	"strings"
	"testing"
)

// Text arriving from outside the buffer may carry "\r\n". The buffer holds
// "\n" only: a "\r" left in it is drawn as ␍ while editing, and in a CRLF file
// used to be saved as "\r\r\n". These pin the client-side origins.

func TestPasteNormalizesLineEndings(t *testing.T) {
	m := newTestModel("x\n")
	m.rpc = &RPC{}
	m.mode = ModeInsert
	updated, _ := m.handlePaste("one\r\ntwo\r\n")
	if got := updated.(Model).buf.Content(); strings.Contains(got, "\r") {
		t.Errorf("a pasted \\r\\n reached the buffer: %q", got)
	}
}

func TestLspEditNormalizesLineEndings(t *testing.T) {
	m := newTestModel("a\nb\n")
	m.rpc = &RPC{}
	m, _ = applyLspEdits(m, []ClientLspEdit{{FromLine: 1, FromCol: 0, ToLine: 1, ToCol: 0, NewText: "inserted\r\n"}})
	if got := m.buf.Content(); strings.Contains(got, "\r") || got != "a\ninserted\nb\n" {
		t.Errorf("buffer = %q, want %q", got, "a\ninserted\nb\n")
	}
}
