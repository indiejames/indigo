package staleprompt

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestKeys(t *testing.T) {
	cases := []struct {
		keys []string
		want Outcome
	}{
		{[]string{"enter"}, QuitRequested}, // Quit is the default
		{[]string{"q"}, QuitRequested},
		{[]string{"esc"}, Dismissed},
		{[]string{"c"}, Dismissed},
		{[]string{"down", "enter"}, Dismissed},
		{[]string{"j", "k", "enter"}, QuitRequested},
		{[]string{"i"}, Pending},
		{[]string{"x"}, Pending},
	}
	for _, c := range cases {
		var p Prompt
		var got Outcome
		for _, k := range c.keys {
			p, got = p.Key(k)
		}
		if got != c.want {
			t.Errorf("%v: outcome %v, want %v", c.keys, got, c.want)
		}
	}
}

func TestRenderFitsNarrowTerminals(t *testing.T) {
	for _, w := range []int{0, 40, 80, 200} {
		out := Prompt{}.Render(w)
		if s := ansi.Strip(out); !strings.Contains(s, "out of date") || !strings.Contains(s, "Continue anyway") {
			t.Errorf("w=%d: title or options missing", w)
		}
		if w >= 40 {
			for _, line := range strings.Split(out, "\n") {
				if lw := ansi.StringWidth(line); lw > w {
					t.Errorf("w=%d: line %d columns wide", w, lw)
				}
			}
		}
	}
}
