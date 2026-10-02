package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// withFakeClipboard points both clipboard seams at an in-process string.
func withFakeClipboard(t *testing.T, content string) *string {
	t.Helper()
	fakeClipboardContent = content
	origReader := clipboardReader
	clipboardReader = func() (string, error) { return fakeClipboardContent, nil }
	t.Cleanup(func() { clipboardReader = origReader })
	return &fakeClipboardContent
}

// threeCursors puts a cursor at the end of each of three lines.
func threeCursors() Model {
	m := newTestModel("a\nb\nc\n")
	m.rpc = &RPC{}
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.extraCursors = []ExtraCursor{
		{pos: document.Pos{Line: 1, Col: 1}, goalCol: -1},
		{pos: document.Pos{Line: 2, Col: 1}, goalCol: -1},
	}
	return m
}

// VS Code's rule, first half: a multi-cursor yank followed by a paste with the
// same number of cursors distributes one piece per cursor, in document order.
func TestPasteDistributesAMatchingMultiCursorYank(t *testing.T) {
	withFakeClipboard(t, "")
	m := threeCursors()
	m.sel = &Selection{Anchor: document.Pos{Line: 0, Col: 0}, Head: document.Pos{Line: 0, Col: 0}}
	m.extraCursors[0].sel = &Selection{Anchor: document.Pos{Line: 1, Col: 0}, Head: document.Pos{Line: 1, Col: 0}}
	m.extraCursors[1].sel = &Selection{Anchor: document.Pos{Line: 2, Col: 0}, Head: document.Pos{Line: 2, Col: 0}}

	y, _ := executeYank(m)
	m = y.(Model)
	if fakeClipboardContent != "a\nb\nc" {
		t.Fatalf("clipboard = %q, want %q", fakeClipboardContent, "a\nb\nc")
	}

	// Cursors back to the end of each line, selections cleared by the yank.
	m.cursor = document.Pos{Line: 0, Col: 1}
	m.extraCursors[0].pos = document.Pos{Line: 1, Col: 1}
	m.extraCursors[1].pos = document.Pos{Line: 2, Col: 1}

	p, _ := executePaste(m)
	got := p.(Model)

	for i, want := range []string{"aa", "bb", "cc"} {
		if got.buf.Line(i) != want {
			t.Errorf("line %d = %q, want %q (its own piece, not the whole clipboard)", i, got.buf.Line(i), want)
		}
	}
}

// VS Code's rule, second half: text the editor did not produce as N selections
// is pasted whole at every cursor, however many lines it has.
func TestPasteRepeatsForeignClipboardAtEveryCursor(t *testing.T) {
	withFakeClipboard(t, "X\nY\nZ")
	m := threeCursors() // multiYank is nil: nothing was yanked here

	p, _ := executePaste(m)
	got := p.(Model)

	// Every line is checked, not just the count and the first: a line count
	// alone is identical however badly the cursors were placed — three
	// three-line inserts add six lines wherever they land. Asserting only
	// that passed against the position bookkeeping this replaced, which put
	// the second and third inserts inside the *first* one's pasted text.
	want := []string{"aX", "Y", "Z", "bX", "Y", "Z", "cX", "Y", "Z", ""}
	if n := got.buf.LineCount(); n != len(want) {
		t.Fatalf("LineCount = %d, want %d", n, len(want))
	}
	for i, w := range want {
		if got.buf.Line(i) != w {
			t.Errorf("line %d = %q, want %q", i, got.buf.Line(i), w)
		}
	}
}

// A cursor count that no longer matches falls back to pasting the whole text,
// rather than distributing the wrong pieces to the wrong cursors.
func TestPasteDoesNotDistributeWhenCursorCountChanged(t *testing.T) {
	withFakeClipboard(t, "p\nq\nr")
	m := threeCursors()
	m.multiYank = []string{"p", "q", "r"}
	m.extraCursors = m.extraCursors[:1] // two cursors now, three pieces

	if parts := m.distributableYank("p\nq\nr"); parts != nil {
		t.Error("distributed with a mismatched cursor count; must paste whole instead")
	}
}

// The clipboard having moved on — something copied in another application —
// also disables distribution. This is what stands in for VS Code's clipboard
// metadata, which the OS clipboard does not carry.
func TestPasteDoesNotDistributeWhenClipboardChanged(t *testing.T) {
	m := threeCursors()
	m.multiYank = []string{"a", "b", "c"}

	if parts := m.distributableYank("something else entirely"); parts != nil {
		t.Error("distributed a clipboard this window did not write")
	}
	if parts := m.distributableYank("a\nb\nc"); parts == nil {
		t.Error("refused to distribute its own unchanged clipboard")
	}
}

// Wiring: executePaste must actually route to the multi-cursor path. Tested
// separately because every assertion above would pass just as happily if
// executePaste still fell through to the single-cursor code.
func TestExecutePasteRoutesToAllCursors(t *testing.T) {
	withFakeClipboard(t, "Z")
	m := threeCursors()

	p, _ := executePaste(m)
	got := p.(Model)

	for i, want := range []string{"aZ", "bZ", "cZ"} {
		if got.buf.Line(i) != want {
			t.Errorf("line %d = %q, want %q: paste reached only the primary cursor", i, got.buf.Line(i), want)
		}
	}
}
