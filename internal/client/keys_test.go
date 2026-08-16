package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
)

// --- findCommand ---

func TestFindCommandSingleKey(t *testing.T) {
	cmd, ok := findCommand([]string{"m"})
	if !ok {
		t.Fatal("findCommand('m') should return ok=true")
	}
	if cmd.key != "m" {
		t.Errorf("cmd.key = %s, want m", cmd.key)
	}
}

func TestFindCommandMultiKey(t *testing.T) {
	cmd, ok := findCommand([]string{"m", "i"})
	if !ok {
		t.Fatal("findCommand('m','i') should return ok=true")
	}
	if cmd.key != "i" {
		t.Errorf("cmd.key = %s, want i", cmd.key)
	}
	if len(cmd.children) == 0 {
		t.Error("mi should have children")
	}
}

func TestFindCommandLeaf(t *testing.T) {
	cmd, ok := findCommand([]string{"m", "i", "w"})
	if !ok {
		t.Fatal("findCommand('m','i','w') should return ok=true")
	}
	if cmd.execute == nil {
		t.Error("miw should have execute func")
	}
}

func TestFindCommandUnknown(t *testing.T) {
	_, ok := findCommand([]string{"ctrl+z"})
	if ok {
		t.Error("findCommand('ctrl+z') should return ok=false")
	}
}

func TestFindCommandEmpty(t *testing.T) {
	cmd, ok := findCommand([]string{})
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
	// 'd' on an empty line: the cursor rests on the line's own line break, so
	// deleting it joins with the next line, leaving a single empty line.
	m := newTestModel("\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("d"))
	got := m2.(Model)
	if got.buf.LineCount() != 1 {
		t.Errorf("LineCount() = %d, want 1", got.buf.LineCount())
	}
}

func TestHandleNormalDeleteEmptyLineAtEOF(t *testing.T) {
	// 'd' on the final, truly-last line with nothing after it is still a
	// no-op: there is no line break there to delete.
	m := newTestModel("")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("d"))
	got := m2.(Model)
	if got.buf.LineCount() != 1 || got.buf.Line(0) != "" {
		t.Errorf("buffer changed on no-op delete: LineCount=%d Line(0)=%q", got.buf.LineCount(), got.buf.Line(0))
	}
}

func TestHandleNormalPrefixCommandM(t *testing.T) {
	m := newTestModel("")
	m2, _ := m.handleNormal(fakeKey("m"))
	got := m2.(Model)
	if len(got.prefixSeq) != 1 || got.prefixSeq[0] != "m" {
		t.Errorf("after m: prefixSeq = %v, want [m]", got.prefixSeq)
	}
}

func TestHandleNormalPrefixEscCancels(t *testing.T) {
	m := newTestModel("")
	m.prefixSeq = []string{"m"}
	m2, _ := m.handleNormal(fakeKey("esc"))
	got := m2.(Model)
	if len(got.prefixSeq) != 0 {
		t.Errorf("esc: prefixSeq = %v, want nil", got.prefixSeq)
	}
}

func TestHandleNormalPrefixMIWExecutes(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 0, Col: 2}
	m.prefixSeq = []string{"m", "i"}
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
	// Regression: indent used to clear the selection, making it impossible to
	// press > repeatedly to indent further without re-selecting.
	if got.sel == nil {
		t.Fatal("indent: sel should be preserved (shifted), not cleared")
	}
	if got.sel.Anchor != (document.Pos{Line: 0, Col: 1}) {
		t.Errorf("indent: sel.Anchor = %v, want {0,1}", got.sel.Anchor)
	}
	if got.sel.Head != (document.Pos{Line: 1, Col: 1}) {
		t.Errorf("indent: sel.Head = %v, want {1,1}", got.sel.Head)
	}
}

// TestIndentTwiceKeepsSelection is a regression test for the reported bug:
// pressing > repeatedly on a selection should keep indenting further each
// time, not require re-selecting after the first press.
func TestIndentTwiceKeepsSelection(t *testing.T) {
	m := newTestModel("aa\nbb\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 0},
		Head:   document.Pos{Line: 1, Col: 0},
	}
	m2, _ := executeIndent(m)
	m3, _ := executeIndent(m2.(Model))
	got := m3.(Model)
	if got.buf.Line(0) != "\t\taa" || got.buf.Line(1) != "\t\tbb" {
		t.Errorf("double indent: Line(0)=%q Line(1)=%q, want \\t\\taa / \\t\\tbb", got.buf.Line(0), got.buf.Line(1))
	}
	if got.sel == nil {
		t.Fatal("indent x2: sel should still be set")
	}
}

// TestIndentNoSelectionShiftsCursor is a regression test: indenting with no
// selection (just a cursor) should shift the cursor along with the inserted
// text, so repeated > keeps the cursor tracking the same character.
func TestIndentNoSelectionShiftsCursor(t *testing.T) {
	m := newTestModel("hello\n")
	m.cursor = document.Pos{Line: 0, Col: 3}
	m2, _ := executeIndent(m)
	got := m2.(Model)
	if got.cursor.Col != 4 {
		t.Errorf("indent: cursor.Col = %d, want 4", got.cursor.Col)
	}
}

// TestUnindentPreservesSelection is a regression test mirroring
// TestIndentMultipleLines: < used to clear the selection too.
func TestUnindentPreservesSelection(t *testing.T) {
	m := newTestModel("\taa\n\tbb\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 0, Col: 2},
		Head:   document.Pos{Line: 1, Col: 2},
	}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(0) != "aa" || got.buf.Line(1) != "bb" {
		t.Errorf("unindent: Line(0)=%q Line(1)=%q, want aa/bb", got.buf.Line(0), got.buf.Line(1))
	}
	if got.sel == nil {
		t.Fatal("unindent: sel should be preserved (shifted), not cleared")
	}
	if got.sel.Anchor != (document.Pos{Line: 0, Col: 1}) {
		t.Errorf("unindent: sel.Anchor = %v, want {0,1}", got.sel.Anchor)
	}
	if got.sel.Head != (document.Pos{Line: 1, Col: 1}) {
		t.Errorf("unindent: sel.Head = %v, want {1,1}", got.sel.Head)
	}
}

// --- Shift+Home / Shift+End select-to-line-start/end ---

func TestExtendLineEndFromCursor(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 2}
	m2, _ := m.handleNormal(fakeKey("shift+end"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("shift+end: sel should be set")
	}
	if got.sel.Anchor != (document.Pos{Line: 0, Col: 2}) {
		t.Errorf("shift+end: sel.Anchor = %v, want {0,2}", got.sel.Anchor)
	}
	if got.sel.Head != (document.Pos{Line: 0, Col: 10}) {
		t.Errorf("shift+end: sel.Head = %v, want {0,10} (last char)", got.sel.Head)
	}
	if got.cursor != got.sel.Head {
		t.Errorf("shift+end: cursor = %v, want %v (sel.Head)", got.cursor, got.sel.Head)
	}
}

func TestExtendLineStartFromCursor(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 7}
	m2, _ := m.handleNormal(fakeKey("shift+home"))
	got := m2.(Model)
	if got.sel == nil {
		t.Fatal("shift+home: sel should be set")
	}
	if got.sel.Anchor != (document.Pos{Line: 0, Col: 7}) {
		t.Errorf("shift+home: sel.Anchor = %v, want {0,7}", got.sel.Anchor)
	}
	if got.sel.Head != (document.Pos{Line: 0, Col: 0}) {
		t.Errorf("shift+home: sel.Head = %v, want {0,0}", got.sel.Head)
	}
	if got.cursor != got.sel.Head {
		t.Errorf("shift+home: cursor = %v, want %v (sel.Head)", got.cursor, got.sel.Head)
	}
}

// TestExtendLineEndExtendsExistingSelection verifies shift+end moves the head
// of an already-active selection rather than starting a fresh one from the
// cursor, mirroring extendWordForward's behavior.
func TestExtendLineEndExtendsExistingSelection(t *testing.T) {
	m := newTestModel("hello world\n")
	m.cursor = document.Pos{Line: 0, Col: 5}
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 2}, Head: document.Pos{Line: 0, Col: 5}}
	m2, _ := m.handleNormal(fakeKey("shift+end"))
	got := m2.(Model)
	if got.sel.Anchor != (document.Pos{Line: 0, Col: 2}) {
		t.Errorf("shift+end: sel.Anchor = %v, want {0,2} (unchanged)", got.sel.Anchor)
	}
	if got.sel.Head != (document.Pos{Line: 0, Col: 10}) {
		t.Errorf("shift+end: sel.Head = %v, want {0,10}", got.sel.Head)
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

// TestIndentUsesDetectedSpaceIndent is a regression test: on a buffer whose
// content is already 2-space indented, > must insert 2 spaces (via
// indentUnit), not a raw tab char, so unindent (<) followed by indent (>)
// round-trips back to the original indentation instead of over-indenting.
func TestIndentUsesDetectedSpaceIndent(t *testing.T) {
	m := newTestModel("if x:\n  hello\n")
	m.detectedIndent = &config.IndentSettings{Style: "spaces", Width: 2}
	m.cursor = document.Pos{Line: 1, Col: 0}
	m2, _ := executeUnindent(m)
	got := m2.(Model)
	if got.buf.Line(1) != "hello" {
		t.Fatalf("unindent: line = %q, want %q", got.buf.Line(1), "hello")
	}
	m3, _ := executeIndent(got)
	got2 := m3.(Model)
	if got2.buf.Line(1) != "  hello" {
		t.Errorf("indent after unindent: line = %q, want %q", got2.buf.Line(1), "  hello")
	}
}

func TestHandleNormalPrefixCommandCapitalM(t *testing.T) {
	m := newTestModel("")
	m2, _ := m.handleNormal(fakeKey("M"))
	got := m2.(Model)
	if len(got.prefixSeq) != 1 || got.prefixSeq[0] != "M" {
		t.Errorf("after M: prefixSeq = %v, want [M]", got.prefixSeq)
	}
}

func TestHandleNormalPrefixMjMovesLineDown(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m.prefixSeq = []string{"M"}
	m2, _ := m.handleNormal(fakeKey("j"))
	got := m2.(Model)
	if got.buf.Line(0) != "bar" || got.buf.Line(1) != "foo" {
		t.Errorf("Mj: Line(0)=%q Line(1)=%q, want bar/foo", got.buf.Line(0), got.buf.Line(1))
	}
	if got.prefixSeq != nil {
		t.Error("Mj: prefixSeq should be cleared after execute")
	}
}

func TestHandleNormalPrefixMkMovesLineUp(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 1, Col: 0}
	m.prefixSeq = []string{"M"}
	m2, _ := m.handleNormal(fakeKey("k"))
	got := m2.(Model)
	if got.buf.Line(0) != "bar" || got.buf.Line(1) != "foo" {
		t.Errorf("Mk: Line(0)=%q Line(1)=%q, want bar/foo", got.buf.Line(0), got.buf.Line(1))
	}
	if got.prefixSeq != nil {
		t.Error("Mk: prefixSeq should be cleared after execute")
	}
}

func TestHandleNormalShiftDownMovesLineDown(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 0, Col: 0}
	m2, _ := m.handleNormal(fakeKey("shift+down"))
	got := m2.(Model)
	if got.buf.Line(0) != "bar" || got.buf.Line(1) != "foo" {
		t.Errorf("shift+down: Line(0)=%q Line(1)=%q, want bar/foo", got.buf.Line(0), got.buf.Line(1))
	}
}

func TestHandleNormalShiftUpMovesLineUp(t *testing.T) {
	m := newTestModel("foo\nbar\n")
	m.cursor = document.Pos{Line: 1, Col: 0}
	m2, _ := m.handleNormal(fakeKey("shift+up"))
	got := m2.(Model)
	if got.buf.Line(0) != "bar" || got.buf.Line(1) != "foo" {
		t.Errorf("shift+up: Line(0)=%q Line(1)=%q, want bar/foo", got.buf.Line(0), got.buf.Line(1))
	}
}

// --- keyRune regression tests ---

// TestKeyRuneModifiedKey verifies that modified plugin keys like "ctrl+p" are
// returned unchanged, not truncated to the first rune. This is a regression
// test for a bug where keyRune would return only "c" for "ctrl+p".
func TestKeyRuneModifiedKey(t *testing.T) {
	result := keyRune("ctrl+p", "Open file picker")
	if result != "ctrl+p" {
		t.Errorf("keyRune(\"ctrl+p\", ...) = %q, want \"ctrl+p\"", result)
	}
}

// TestKeyRuneEmptyKeyUsesLabel verifies that when key is empty, keyRune
// derives the result from the first rune of the lowercased label.
func TestKeyRuneEmptyKeyUsesLabel(t *testing.T) {
	result := keyRune("", "Symbol picker")
	if result != "s" {
		t.Errorf("keyRune(\"\", \"Symbol picker\") = %q, want \"s\"", result)
	}
}

// TestKeyRuneSingleChar verifies that single-character keys are returned unchanged.
func TestKeyRuneSingleChar(t *testing.T) {
	result := keyRune("p", "Paste")
	if result != "p" {
		t.Errorf("keyRune(\"p\", \"Paste\") = %q, want \"p\"", result)
	}
}

// TestKeyRuneEmptyBoth verifies that when both key and label are empty, an empty string is returned.
func TestKeyRuneEmptyBoth(t *testing.T) {
	result := keyRune("", "")
	if result != "" {
		t.Errorf("keyRune(\"\", \"\") = %q, want \"\"", result)
	}
}

// TestClientMenuItemsToCommandsModifiedKey verifies that plugin menu items with
// modified keys (e.g., "ctrl+p") are correctly converted to commands with the
// full key preserved for both display and dispatch.
func TestClientMenuItemsToCommandsModifiedKey(t *testing.T) {
	items := []ClientMenuItem{
		{
			Key:        "ctrl+p",
			Label:      "Open file picker",
			PluginName: "test-plugin",
			Command:    "open-picker",
		},
	}

	cmds := clientMenuItemsToCommands(items)
	if len(cmds) != 1 {
		t.Fatalf("clientMenuItemsToCommands returned %d commands, want 1", len(cmds))
	}

	cmd := cmds[0]
	if cmd.key != "ctrl+p" {
		t.Errorf("command.key = %q, want \"ctrl+p\"", cmd.key)
	}
	if cmd.label != "Open file picker" {
		t.Errorf("command.label = %q, want \"Open file picker\"", cmd.label)
	}
	if cmd.execute == nil {
		t.Error("command.execute should be set for leaf menu items")
	}
}

// TestClientMenuItemsToCommandsEmptyKey verifies that when a plugin menu item
// has an empty key, the first rune of the lowercased label is used.
func TestClientMenuItemsToCommandsEmptyKey(t *testing.T) {
	items := []ClientMenuItem{
		{
			Key:        "",
			Label:      "Symbol picker",
			PluginName: "test-plugin",
			Command:    "symbol-picker",
		},
	}

	cmds := clientMenuItemsToCommands(items)
	if len(cmds) != 1 {
		t.Fatalf("clientMenuItemsToCommands returned %d commands, want 1", len(cmds))
	}

	cmd := cmds[0]
	if cmd.key != "s" {
		t.Errorf("command.key = %q, want \"s\" (first rune of lowercased label)", cmd.key)
	}
}
