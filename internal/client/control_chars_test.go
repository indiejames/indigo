package client

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestControlCharactersAreDrawnVisibly is the regression for a CRLF file
// rendering as scattered fragments: buffer text was written to the terminal
// verbatim, so a "\r" sent the cursor back to column 0 and the rest of the row
// overwrote the line. A file with mixed endings still has its "\r"s in the
// buffer, and any control character — ESC most dangerously — must be shown,
// not obeyed.
func TestControlCharactersAreDrawnVisibly(t *testing.T) {
	m := newTestModel("before\rafter\nclear\x1b[2Jscreen\n")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 10})
	out := updated.(Model).View().Content

	if strings.Contains(out, "\r") {
		t.Error("a carriage return from the file reached the terminal")
	}
	if strings.Contains(out, "\x1b[2J") {
		t.Error("an escape sequence from the file reached the terminal (it would clear the screen)")
	}
	for _, want := range []string{"before␍after", "clear␛[2Jscreen"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered output lacks %q", want)
		}
	}
}

func TestDisplayRune(t *testing.T) {
	for in, want := range map[rune]rune{
		'\r': '␍', 0x1b: '␛', 0x00: '␀', 0x7f: '␡', 0x9b: '�',
		'\t': '\t', 'a': 'a', 'é': 'é', '世': '世',
	} {
		if got := displayRune(in); got != want {
			t.Errorf("displayRune(%U) = %U, want %U", in, got, want)
		}
	}
}
