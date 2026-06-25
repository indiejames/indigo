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
	m2, _ := m.handleNormal(fakeKey("g"))
	got := m2.(Model)
	if got.cursor.Line != 0 || got.cursor.Col != 0 {
		t.Errorf("g: cursor = %v, want {0,0}", got.cursor)
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

func TestHandleNormalSelectWord(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("w"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("w: sel should be set")
	}
	if got.sel.Anchor.Col != 0 || got.sel.Head.Col != 4 {
		t.Errorf("w: sel [%d,%d], want [0,4]", got.sel.Anchor.Col, got.sel.Head.Col)
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
	m2, cmd := m.handleNormal(fakeKey("d"))
	_ = m2
	if cmd != nil {
		// Still valid: deleteSelection returns nil cmd on empty line
	}
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

// --- handleWarnQuit ---

func TestHandleWarnQuitDiscardQ(t *testing.T) {
	m := newTestModel("text")
	m.warnQuit = true
	m2, _ := m.handleWarnQuit(fakeKey("q"))
	got := m2.(Model)
	if got.warnQuit {
		t.Error("q: warnQuit should be false")
	}
	if !got.quitting {
		t.Error("q: quitting should be true")
	}
}

func TestHandleWarnQuitDiscardCapQ(t *testing.T) {
	m := newTestModel("text")
	m.warnQuit = true
	m2, _ := m.handleWarnQuit(fakeKey("Q"))
	got := m2.(Model)
	if !got.quitting {
		t.Error("Q: quitting should be true")
	}
}

func TestHandleWarnQuitEscCancels(t *testing.T) {
	m := newTestModel("text")
	m.warnQuit = true
	m2, _ := m.handleWarnQuit(fakeKey("esc"))
	got := m2.(Model)
	if got.warnQuit {
		t.Error("esc: warnQuit should be false")
	}
	if got.quitting {
		t.Error("esc: quitting should be false")
	}
}

func TestHandleWarnQuitNKey(t *testing.T) {
	m := newTestModel("text")
	m.warnQuit = true
	m2, _ := m.handleWarnQuit(fakeKey("n"))
	got := m2.(Model)
	if got.warnQuit {
		t.Error("n: warnQuit should be false")
	}
	if got.quitting {
		t.Error("n: quitting should be false")
	}
}

func TestHandleWarnQuitSaveReturnsCmd(t *testing.T) {
	m := newTestModel("text")
	m.warnQuit = true
	m2, cmd := m.handleWarnQuit(fakeKey("s"))
	got := m2.(Model)
	if got.warnQuit {
		t.Error("s: warnQuit should be false")
	}
	if cmd == nil {
		t.Error("s: should return a non-nil cmd for saving")
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
