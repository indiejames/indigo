package server

import (
	"testing"

	"github.com/indiejames/indigo/internal/debug"
	"github.com/indiejames/indigo/internal/document"
)

// Edits the server makes itself — workspace edits, plugin edits, moving text
// between files — go through applyServerOriginated, not ApplyOp. They must move
// breakpoints too, or a rename that inserts a line leaves every breakpoint
// below it one line off.
func TestServerOriginatedEditShiftsBreakpoints(t *testing.T) {
	s := &editorService{dbg: debug.NewManager(nil)}
	const path = "/w/a.go"
	entry := &bufferEntry{
		buf:       document.New(path, "a\nb\nc\n"),
		canonPath: path,
		onApplied: s.shiftBreakpoints,
	}
	s.dbg.ToggleBreakpoint(path, 2)

	applyServerOriginated(entry, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 0, InsertText: "new\n"})

	bps, _ := s.dbg.Breakpoints.List(path)
	if len(bps) != 1 || bps[0].Line != 3 {
		t.Errorf("breakpoints after a server edit = %+v, want line 3", bps)
	}
}

// An entry with no hook (hand-built in tests, or a throwaway on-disk edit)
// must not panic.
func TestServerOriginatedEditWithoutHook(t *testing.T) {
	entry := &bufferEntry{buf: document.New("/w/b.go", "a\n")}
	applyServerOriginated(entry, 0, document.Op{Type: document.OpInsert, InsertLine: 0, InsertText: "x\n"})
}
