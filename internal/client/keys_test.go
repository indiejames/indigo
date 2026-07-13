package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// --- findCommand ---

func TestFindCommandSingleKey(t *testing.T) {
	cmd, ok := findCommand([]rune{'m'})
	if !ok {
		t.Fatal("findCommand('m') should return ok=true")
	}
	if cmd.key != 'm' {
		t.Errorf("cmd.key = %c, want m", cmd.key)
	}
}

func TestFindCommandMultiKey(t *testing.T) {
	cmd, ok := findCommand([]rune{'m', 'i'})
	if !ok {
		t.Fatal("findCommand('m','i') should return ok=true")
	}
	if cmd.key != 'i' {
		t.Errorf("cmd.key = %c, want i", cmd.key)
	}
	if len(cmd.children) == 0 {
		t.Error("mi should have children")
	}
}

func TestFindCommandLeaf(t *testing.T) {
	cmd, ok := findCommand([]rune{'m', 'i', 'w'})
	if !ok {
		t.Fatal("findCommand('m','i','w') should return ok=true")
	}
	if cmd.execute == nil {
		t.Error("miw should have execute func")
	}
}

func TestFindCommandUnknown(t *testing.T) {
	_, ok := findCommand([]rune{'z'})
	if ok {
		t.Error("findCommand('z') should return ok=false")
	}
}

func TestFindCommandEmpty(t *testing.T) {
	cmd, ok := findCommand([]rune{})
	if ok || cmd != nil {
		t.Error("findCommand(empty) should return nil, false")
	}
}

// --- handleNormal movement ---

func TestHandleNormalMoveDown(t *testing.T) {
	m := newTestModel("line1\nline2\nline3\n")
	m2, _ := m.handleNormal(fakeKey("j"))
	got := m2.(Model)
	if got.cursor.Line != 1 {
		t.Errorf("j: cursor.Line = %d, want 1", got.cursor.Line)
	}
}

func TestHandleNormalMoveUp(t *testing.T) {
	m := newTestModel("line1\nline2\n")
	m.cursor.Line = 1
	m2, _ := m.handleNormal(fakeKey("k"))
	got := m2.(Model)
	if got.cursor.Line != 0 {
		t.Errorf("k: cursor.Line = %d, want 0", got.cursor.Line)
	}
}

func TestHandleNormalMoveRight(t *testing.T) {
	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("l"))
	got := m2.(Model)
	if got.cursor.Col != 1 {
		t.Errorf("l: cursor.Col = %d, want 1", got.cursor.Col)
	}
}

func TestHandleNormalMoveLeft(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor.Col = 2
	m2, _ := m.handleNormal(fakeKey("h"))
	got := m2.(Model)
	if got.cursor.Col != 1 {
		t.Errorf("h: cursor.Col = %d, want 1", got.cursor.Col)
	}
}

func TestHandleNormalGotoTop(t *testing.T) {
	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 2
	// First 'g' enters the prefix sequence.
	m2, _ := m.handleNormal(fakeKey("g"))
	mid := m2.(Model)
	if mid.cursor.Line != 2 {
		t.Errorf("g (first): cursor should not move yet, got line %d", mid.cursor.Line)
	}
	// Second 'g' executes gg → go to top.
	m3, _ := mid.handleNormal(fakeKey("g"))
	got := m3.(Model)
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("gg: cursor = %v, want {0,0}", got.cursor)
	}
}

func TestHandleNormalGotoBottom(t *testing.T) {
	m := newTestModel("a\nb\nc\n")
	m2, _ := m.handleNormal(fakeKey("G"))
	got := m2.(Model)
	last := got.buf.LineCount() - 1
	if got.cursor.Line != last {
		t.Errorf("G: cursor.Line = %d, want %d", got.cursor.Line, last)
	}
}

func TestHandleNormalGotoCol0(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor.Col = 3
	m2, _ := m.handleNormal(fakeKey("0"))
	got := m2.(Model)
	if got.cursor.Col != 0 {
		t.Errorf("0: cursor.Col = %d, want 0", got.cursor.Col)
	}
}

func TestHandleNormalEnterCommandMode(t *testing.T) {
	m := newTestModel("")
	m2, _ := m.handleNormal(fakeKey(":"))
	got := m2.(Model)
	if got.mode != ModeCommand {
		t.Errorf(": mode = %v, want ModeCommand", got.mode)
	}
	if got.cmdBuf != "" {
		t.Errorf(": cmdBuf = %q, want empty", got.cmdBuf)
	}
}

func TestHandleNormalEnterInsertMode(t *testing.T) {
	m := newTestModel("hello\n")
	m.sel = &Selection{Anchor: document.Pos{}, Head: document.Pos{Col: 2}}
	m2, _ := m.handleNormal(fakeKey("i"))
	got := m2.(Model)
	if got.mode != ModeInsert {
		t.Errorf("i: mode = %v, want ModeInsert", got.mode)
	}
	if got.sel != nil {
		t.Error("i: selection should be cleared")
	}
}

func TestHandleNormalEscClearsSelection(t *testing.T) {
	m := newTestModel("hello\n")
	m.sel = &Selection{Anchor: document.Pos{}, Head: document.Pos{Col: 4}}
	m2, _ := m.handleNormal(fakeKey("esc"))
	got := m2.(Model)
	if got.sel != nil {
		t.Error("esc: selection should be nil")
	}
}

func TestHandleNormalNextWordStart(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("w"))
	got := m2.(Model)
	if got.sel != nil {
		t.Error("w: sel should be nil (w is navigation, not selection)")
	}
	if got.cursor.Col != 6 {
		t.Errorf("w: cursor col = %d, want 6 (start of 'world')", got.cursor.Col)
	}
}

func TestHandleNormalSelectLine(t *testing.T) {
	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("x"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("x: sel should be set")
	}
	if !got.sel.IsLine {
		t.Error("x: sel.IsLine should be true")
	}
}

func TestHandleNormalDeleteEmptyLine(t *testing.T) {
	// 'd' on empty line → no-op (no RPC needed)
	m := newTestModel("\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("d"))
	_ = m2
}

func TestHandleNormalPrefixCommandM(t *testing.T) {
	m := newTestModel("")
	m2, _ := m.handleNormal(fakeKey("m"))
	got := m2.(Model)
	if len(got.prefixSeq) != 1 || got.prefixSeq[0] != 'm' {
		t.Errorf("after m: prefixSeq = %v, want [m]", got.prefixSeq)
	}
}

func TestHandleNormalPrefixEscCancels(t *testing.T) {
	m := newTestModel("")
	m.prefixSeq = []rune{'m'}
	m2, _ := m.handleNormal(fakeKey("esc"))
	got := m2.(Model)
	if len(got.prefixSeq) != 0 {
		t.Errorf("esc: prefixSeq = %v, want nil", got.prefixSeq)
	}
}

func TestHandleNormalPrefixMIWExecutes(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 0, Col: 2}
	m.prefixSeq = []rune{'m', 'i'}
	m2, _ := m.handleNormal(fakeKey("w"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("miw: selection should be set")
	}
	if got.prefixSeq != nil {
		t.Error("miw: prefixSeq should be cleared after execute")
	}
}

// --- handleInsert (safe: esc and movement only) ---

func TestHandleInsertEscToNormal(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 3
	m2, _ := m.handleInsert(fakeKey("esc"))
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("esc: mode = %v, want ModeNormal", got.mode)
	}
	if got.cursor.Col != 2 {
		t.Errorf("esc: cursor.Col = %d, want 2 (decremented)", got.cursor.Col)
	}
}

func TestHandleInsertEscAtCol0(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 0
	m2, _ := m.handleInsert(fakeKey("esc"))
	got := m2.(Model)
	if got.cursor.Col != 0 {
		t.Errorf("esc at col 0: cursor.Col = %d, want 0 (no decrement)", got.cursor.Col)
	}
}

func TestHandleInsertEscCommitsUndoGroup(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.currentGroup = []document.Op{{Type: document.OpInsert}} // fake group
	m2, _ := m.handleInsert(fakeKey("esc"))
	got := m2.(Model)
	if len(got.undoStack) != 1 {
		t.Errorf("esc: undoStack len = %d, want 1", len(got.undoStack))
	}
	if got.currentGroup != nil {
		t.Error("esc: currentGroup should be nil after commit")
	}
}

// TestJumpListRecordsInsertStart is a regression test: the EditRecordMsg emitted
// when leaving insert mode must carry the cursor line where insert mode was
// entered, not the line where the cursor ended up after typing.
func TestJumpListRecordsInsertStart(t *testing.T) {
	m := newTestModel("line0\nline1\nline2\nline3\nline4\n")
	m.cursor = document.Pos{Line: 2, Col: 0}
	m.mode = ModeInsert
	m.currentGroup = []document.Op{}
	m.groupBefore = m.cursorSnap() // start of insert session: line 2
	m.insertLineCount = m.buf.LineCount()

	// Simulate typing that advanced the cursor to line 4 (e.g. newlines inserted).
	m.cursor = document.Pos{Line: 4, Col: 3}
	m.currentGroup = append(m.currentGroup, document.Op{Type: document.OpInsert})

	_, cmd := m.handleInsert(fakeKey("esc"))
	if cmd == nil {
		t.Fatal("expected recordCmd, got nil")
	}
	msg := cmd()
	rec, ok := msg.(EditRecordMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want EditRecordMsg", msg)
	}
	if rec.Line != 2 {
		t.Errorf("EditRecordMsg.Line = %d, want 2 (insert session start line)", rec.Line)
	}
}

func TestHandleInsertMoveRight(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m2, _ := m.handleInsert(fakeKey("right"))
	got := m2.(Model)
	if got.cursor.Col != 1 {
		t.Errorf("right: cursor.Col = %d, want 1", got.cursor.Col)
	}
}

func TestHandleInsertHome(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 3
	m2, _ := m.handleInsert(fakeKey("home"))
	got := m2.(Model)
	if got.cursor.Col != 0 {
		t.Errorf("home: cursor.Col = %d, want 0", got.cursor.Col)
	}
}

func TestHandleInsertEnd(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m2, _ := m.handleInsert(fakeKey("end"))
	got := m2.(Model)
	if got.cursor.Col != 5 { // lineLen of "hello" = 5
		t.Errorf("end: cursor.Col = %d, want 5", got.cursor.Col)
	}
}


// --- executeSelectInsideWord ---

func TestExecuteSelectInsideWordMid(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 2} // mid "hello"
	m2, _ := executeSelectInsideWord(m)
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("miw: sel should be set")
	}
	if got.sel.Anchor.Col != 0 || got.sel.Head.Col != 4 {
		t.Errorf("miw: sel [%d,%d], want [0,4]", got.sel.Anchor.Col, got.sel.Head.Col)
	}
}

func TestExecuteSelectInsideWordOnNonWord(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 5} // on space
	m2, _ := executeSelectInsideWord(m)
	got := m2.(Model)
	if got.sel != nil {
		t.Error("miw on space: sel should be nil")
	}
}

func TestExecuteSelectInsideWordEmptyLine(t *testing.T) {
	m := newTestModel("\n")
	m2, _ := executeSelectInsideWord(m)
	got := m2.(Model)
	if got.sel != nil {
		t.Error("miw on empty line: sel should be nil")
	}
}

// --- z / Z mark-based selection ---

func TestMarkSetZ(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 3}
	m2, _ := m.handleNormal(fakeKey("z"))
	got := m2.(Model)
	if got.mark == nil {
		t.Fatal("z: mark should be set")
	}
	if *got.mark != (document.Pos{Line: 0, Col: 3}) {
		t.Errorf("z: mark = %v, want {0,3}", *got.mark)
	}
}

func TestMarkSelectZ(t *testing.T) {
	m := newTestModel("hello world\n")
	mark := document.Pos{Line: 0, Col: 2}
	m.mark = &mark
	m.cursor = document.Pos{Line: 0, Col: 7}
	m2, _ := m.handleNormal(fakeKey("Z"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("Z: sel should be set after mark+jump")
	}
	if got.sel.Anchor != mark {
		t.Errorf("Z: sel.Anchor = %v, want %v", got.sel.Anchor, mark)
	}
	if got.sel.Head != m.cursor {
		t.Errorf("Z: sel.Head = %v, want %v", got.sel.Head, m.cursor)
	}
}

func TestMarkSelectZNoMark(t *testing.T) {
	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("Z"))
	got := m2.(Model)
	if got.sel != nil {
		t.Error("Z with no mark: sel should remain nil")
	}
	if got.status == "" {
		t.Error("Z with no mark: status should describe the error")
	}
}

// --- > indent / < unindent ---

func TestIndentCurrentLine(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := executeIndent(m)
	got := m2.(Model)
	line := got.buf.Line(0)
	if line != "\thello" {
		t.Errorf("indent: line = %q, want %q", line, "\thello")
	}
}

func TestIndentMultipleLines(t *testing.T) {
	m := newTestModel("aa\nbb\ncc\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
	}
	m2, _ := executeIndent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "\taa" {
		t.Errorf("indent line 0: got %q, want %q", got.buf.Line(0), "\taa")
	}
	if got.buf.Line(1) != "\tbb" {
		t.Errorf("indent line 1: got %q, want %q", got.buf.Line(1), "\tbb")
	}
	if got.buf.Line(2) != "cc" {
		t.Errorf("indent line 2 should be unchanged, got %q", got.buf.Line(2))
	}
	if got.sel != nil {
		t.Error("indent: sel should be cleared after indent")
	}
}

func TestUnindentTab(t *testing.T) {
	m := newTestModel("\thello\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "hello" {
		t.Errorf("unindent tab: got %q, want %q", got.buf.Line(0), "hello")
	}
}

func TestUnindentSpaces(t *testing.T) {
	m := newTestModel("    hello\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "hello" {
		t.Errorf("unindent spaces: got %q, want %q", got.buf.Line(0), "hello")
	}
}

func TestUnindentPartialSpaces(t *testing.T) {
	m := newTestModel("  hello\n") // only 2 spaces
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "hello" {
		t.Errorf("unindent 2 spaces: got %q, want %q", got.buf.Line(0), "hello")
	}
}

func TestUnindentNoIndent(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "hello" {
		t.Errorf("unindent with no indent: line changed unexpectedly to %q", got.buf.Line(0))
	}
}
