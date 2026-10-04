package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// mdModel is a markdown buffer in insert mode with the cursor at the end of
// the given line.
func mdModel(t *testing.T, content string, line int) Model {
	t.Helper()
	m := newTestModel(content)
	m.rpc = &RPC{}
	m.mode = ModeInsert
	m.buf = document.New("notes.md", content)
	m.filePath = "notes.md"
	m.cursor = document.Pos{Line: line, Col: len([]rune(m.buf.Line(line)))}
	return m
}

func pressEnter(t *testing.T, m Model) Model {
	t.Helper()
	out, _ := m.handleEnter()
	return out
}

func TestMarkdownListContinuation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		line     string
		wantNext string
	}{
		{"ordered increments", "1. first", "2. "},
		{"ordered keeps its own number", "7. seventh", "8. "},
		{"ordered paren delimiter", "1) first", "2) "},
		{"dash bullet", "- item", "- "},
		{"star bullet", "* item", "* "},
		{"plus bullet", "+ item", "+ "},
		{"indentation is kept", "   - nested", "   - "},
		{"tab indentation is kept", "\t- nested", "\t- "},
		{"wide gap is kept", "-   spaced", "-   "},
		{"task item continues unchecked", "- [ ] todo", "- [ ] "},
		{"done task continues unchecked", "- [x] done", "- [ ] "},
		{"double digits increment", "10. tenth", "11. "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pressEnter(t, mdModel(t, tc.line+"\n", 0))
			if got.buf.Line(0) != tc.line {
				t.Errorf("line 0 = %q, want it untouched (%q)", got.buf.Line(0), tc.line)
			}
			if got.buf.Line(1) != tc.wantNext {
				t.Errorf("line 1 = %q, want %q", got.buf.Line(1), tc.wantNext)
			}
			if got.cursor.Line != 1 || got.cursor.Col != len([]rune(tc.wantNext)) {
				t.Errorf("cursor = %d:%d, want 1:%d (after the new marker)",
					got.cursor.Line, got.cursor.Col, len([]rune(tc.wantNext)))
			}
		})
	}
}

// An item with no content ends the list: the marker goes and the cursor stays
// on the blank line. Without this there is no way out of a list.
func TestMarkdownEmptyItemEndsTheList(t *testing.T) {
	for _, tc := range []struct{ name, line, want string }{
		{"bullet", "- ", ""},
		{"ordered", "3. ", ""},
		{"marker with no trailing space", "-", ""},
		{"empty checkbox", "- [ ] ", ""},
		{"nested keeps its indent", "  - ", "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pressEnter(t, mdModel(t, tc.line+"\nafter\n", 0))
			if got.buf.Line(0) != tc.want {
				t.Errorf("line 0 = %q, want %q (marker removed)", got.buf.Line(0), tc.want)
			}
			if got.buf.Line(1) != "after" {
				t.Errorf("line 1 = %q, want %q — no line should have been added", got.buf.Line(1), "after")
			}
			if got.cursor.Line != 0 || got.cursor.Col != len([]rune(tc.want)) {
				t.Errorf("cursor = %d:%d, want 0:%d", got.cursor.Line, got.cursor.Col, len([]rune(tc.want)))
			}
		})
	}
}

// Things that look like markers but are not.
func TestMarkdownNonListLinesFallThrough(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"emphasis", "*italic*"},
		{"bullet with no gap", "-text"},
		{"ordered with no gap", "1.text"},
		{"decimal number", "1.5 apples"},
		{"a year", "2026. was a year"},
		{"plain prose", "just some text"},
		{"heading", "# Title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pressEnter(t, mdModel(t, tc.line+"\n", 0))
			if got.buf.Line(1) != "" {
				t.Errorf("line 1 = %q, want empty: %q is not a list item", got.buf.Line(1), tc.line)
			}
		})
	}
}

// Only markdown. The same line in a C file must get a plain newline — "*" is
// a comment continuation there, and "1." is not a list anywhere else either.
func TestListContinuationIsMarkdownOnly(t *testing.T) {
	m := newTestModel("* item\n")
	m.rpc = &RPC{}
	m.mode = ModeInsert
	m.buf = document.New("main.c", "* item\n")
	m.filePath = "main.c"
	m.cursor = document.Pos{Line: 0, Col: 6}

	got := pressEnter(t, m)
	if got.buf.Line(1) != "" {
		t.Errorf("line 1 = %q, want empty: list continuation must not apply to .c", got.buf.Line(1))
	}
}

// ":set ft=md" turns it on for a file whose name says nothing, matching how
// indentation already treats the override.
func TestListContinuationHonoursFileTypeOverride(t *testing.T) {
	m := newTestModel("- item\n")
	m.rpc = &RPC{}
	m.mode = ModeInsert
	m.buf = document.New("CHANGELOG", "- item\n")
	m.filePath = "CHANGELOG"
	m.cursor = document.Pos{Line: 0, Col: 6}

	if got := pressEnter(t, m); got.buf.Line(1) != "" {
		t.Fatalf("line 1 = %q, want empty before the override is set", got.buf.Line(1))
	}

	m.langOverride = "md"
	if got := pressEnter(t, m); got.buf.Line(1) != "- " {
		t.Errorf("line 1 = %q, want %q with ft=md set", got.buf.Line(1), "- ")
	}
}

// Enter in the middle of an item splits it without inventing a marker.
func TestMarkdownEnterMidItemDoesNotContinue(t *testing.T) {
	m := mdModel(t, "- hello world\n", 0)
	m.cursor = document.Pos{Line: 0, Col: 7} // between "hello " and "world"

	got := pressEnter(t, m)

	if got.buf.Line(0) != "- hello" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "- hello")
	}
	if got.buf.Line(1) != " world" {
		t.Errorf("line 1 = %q, want %q (no marker added mid-item)", got.buf.Line(1), " world")
	}
}

// The end-of-line guard compares a rune column against the line's length, so
// that length has to be in runes too. With a byte length, any non-ASCII
// character earlier in the item makes the cursor look mid-item and the list
// stops continuing — one accented word is enough.
func TestMarkdownListContinuesWithNonASCIIContent(t *testing.T) {
	for _, tc := range []struct{ name, line, wantNext string }{
		{"accented word", "- café", "- "},
		{"ordered with diaeresis", "1. naïve", "2. "},
		{"multibyte beyond latin1", "- 日本語", "- "},
		{"emoji", "* done ✅", "* "},
		{"task item", "- [ ] café", "- [ ] "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pressEnter(t, mdModel(t, tc.line+"\n", 0))
			if got.buf.Line(0) != tc.line {
				t.Errorf("line 0 = %q, want it untouched (%q)", got.buf.Line(0), tc.line)
			}
			if got.buf.Line(1) != tc.wantNext {
				t.Errorf("line 1 = %q, want %q", got.buf.Line(1), tc.wantNext)
			}
			if got.cursor.Line != 1 || got.cursor.Col != len([]rune(tc.wantNext)) {
				t.Errorf("cursor = %d:%d, want 1:%d", got.cursor.Line, got.cursor.Col, len([]rune(tc.wantNext)))
			}
		})
	}
}

// The guard must still hold for a non-ASCII item: Enter before the end splits
// without inventing a marker. Pinned so the fix above cannot be "drop the
// guard".
func TestMarkdownNonASCIIMidItemDoesNotContinue(t *testing.T) {
	m := mdModel(t, "- café au lait\n", 0)
	m.cursor = document.Pos{Line: 0, Col: 6} // right after "café", mid-item

	got := pressEnter(t, m)

	if got.buf.Line(0) != "- café" {
		t.Errorf("line 0 = %q, want %q", got.buf.Line(0), "- café")
	}
	if got.buf.Line(1) != " au lait" {
		t.Errorf("line 1 = %q, want %q (no marker mid-item)", got.buf.Line(1), " au lait")
	}
}
