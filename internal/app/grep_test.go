package app

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestGrepResultsMsgDiscardsStaleSequence(t *testing.T) {
	a := App{grep: &grepPicker{searching: true, seq: 2}}

	// A result for the superseded request (seq 1) must not be applied.
	updated, _ := a.Update(grepResultsMsg{seq: 1, results: []GrepResult{{RelPath: "stale.go"}}})
	a = updated.(App)
	if len(a.grep.results) != 0 {
		t.Fatalf("stale seq=1 result was applied: %+v, want no results applied", a.grep.results)
	}
	if !a.grep.searching {
		t.Error("searching should still be true — the current request (seq 2) hasn't answered yet")
	}

	// The result for the current request (seq 2) must be applied.
	updated, _ = a.Update(grepResultsMsg{seq: 2, results: []GrepResult{{RelPath: "current.go"}}})
	a = updated.(App)
	if len(a.grep.results) != 1 || a.grep.results[0].RelPath != "current.go" {
		t.Errorf("results = %+v, want the current request's result applied", a.grep.results)
	}
	if a.grep.searching {
		t.Error("searching should be false once the current request's result has landed")
	}
}

func TestGrepResultLine(t *testing.T) {
	r := GrepResult{RelPath: "cmd/main.go", Line: 41, Col: 0, LineText: "	fmt.Println(\"hello\")"}
	line := grepResultLine(r, 80)
	if line == "" {
		t.Error("expected non-empty result line")
	}
	// Should contain the file path and 1-based line number.
	if !contains(line, "cmd/main.go") || !contains(line, "42:") {
		t.Errorf("result line missing path/number: %q", line)
	}
}

func TestGrepResultLineKeepsMatchVisibleWhenFarIntoLine(t *testing.T) {
	// A match near the start of a long line still uses the old plain
	// right-truncate.
	near := GrepResult{RelPath: "a.go", Line: 0, Col: 0, MatchLen: 3, LineText: "foo" + strings.Repeat("x", 200)}
	nearLine := grepResultLine(near, 40)
	if !contains(nearLine, "foo") {
		t.Errorf("match near line start should stay visible: %q", nearLine)
	}
	if !strings.HasSuffix(nearLine, "…") {
		t.Errorf("expected a trailing ellipsis for a right-truncated line: %q", nearLine)
	}

	// A match far into a long line must still be visible, not scrolled
	// past a truncated-from-the-start window.
	pad := strings.Repeat("x", 200)
	far := GrepResult{RelPath: "a.go", Line: 0, Col: len(pad), MatchLen: 5, LineText: pad + "NEEDLE" + strings.Repeat("y", 200)}
	farLine := grepResultLine(far, 40)
	if !contains(farLine, "NEEDL") { // MatchLen=5 covers "NEEDL", not the trailing "E"
		t.Errorf("match far into line was not kept visible: %q", farLine)
	}
	if !strings.HasPrefix(farLine, "a.go:1: ") {
		t.Errorf("expected the label to stay intact: %q", farLine)
	}
	if lipgloss.Width(farLine) > 40 {
		t.Errorf("result line exceeded maxW=40: %q (display width %d)", farLine, lipgloss.Width(farLine))
	}
}

func TestGrepResultLineBoldsMatch(t *testing.T) {
	r := GrepResult{RelPath: "a.go", Line: 0, Col: 4, MatchLen: 5, LineText: "foo MATCH bar"}
	line := grepResultLine(r, 80)
	want := "foo \x1b[1mMATCH\x1b[22m bar"
	if !strings.Contains(line, want) {
		t.Errorf("grepResultLine(%+v) = %q, want it to contain %q", r, line, want)
	}
}

func TestGrepPickerViewBoldsMatchWithoutBreakingLayout(t *testing.T) {
	gp := &grepPicker{
		pattern: "MATCH",
		width:   60,
		height:  20,
		results: []GrepResult{{
			RelPath:  "a.go",
			Line:     0,
			Col:      40,
			MatchLen: 5,
			LineText: strings.Repeat("a", 40) + "MATCH" + strings.Repeat("b", 40),
		}},
	}
	out := gp.View()
	if !strings.Contains(out, "\x1b[1m") {
		t.Fatalf("expected the match to be bolded in the rendered picker, got:\n%s", out)
	}

	// Every box row (title/hint/results, but not the full-width blank
	// filler rows used to vertically center the box) should have the same
	// display width — a rune-count (rather than ANSI-aware) clamp would
	// miscount the row carrying the bold escape codes and pad/truncate it
	// differently from the rest.
	blankFiller := strings.Repeat(" ", gp.width)
	var widths []int
	for _, l := range strings.Split(out, "\n") {
		if l == "" || l == blankFiller {
			continue
		}
		widths = append(widths, lipgloss.Width(l))
	}
	if len(widths) == 0 {
		t.Fatal("expected at least one non-blank rendered row")
	}
	for i := 1; i < len(widths); i++ {
		if widths[i] != widths[0] {
			t.Errorf("box row widths inconsistent (embedded ANSI likely miscounted by clamp): row 0 = %d, row %d = %d", widths[0], i, widths[i])
		}
	}
}

func TestGrepResultLineWideContext(t *testing.T) {
	// Wide (2-cell) CJK runes surrounding an ASCII match: a rune-count
	// budget would let the rendered line silently exceed maxW.
	r := GrepResult{
		RelPath:  "a.go",
		Line:     0,
		Col:      20,
		MatchLen: 5,
		LineText: strings.Repeat("字", 20) + "MATCH" + strings.Repeat("符", 20),
	}
	for _, maxW := range []int{10, 20, 30, 40, 60, 80} {
		line := grepResultLine(r, maxW)
		if w := lipgloss.Width(line); w > maxW {
			t.Errorf("grepResultLine maxW=%d produced display width %d: %q", maxW, w, line)
		}
	}
}

func TestGrepResultLineWideMatch(t *testing.T) {
	// The match itself is made of wide runes.
	r := GrepResult{
		RelPath:  "a.go",
		Line:     0,
		Col:      10,
		MatchLen: 3,
		LineText: strings.Repeat("a", 10) + "中文字" + strings.Repeat("b", 10),
	}
	for _, maxW := range []int{8, 10, 15, 20, 30, 50} {
		line := grepResultLine(r, maxW)
		if w := lipgloss.Width(line); w > maxW {
			t.Errorf("grepResultLine maxW=%d produced display width %d: %q", maxW, w, line)
		}
	}
	if line := grepResultLine(r, 30); !strings.Contains(line, "中文字") {
		t.Errorf("expected the wide match to remain visible at a comfortable maxW, got %q", line)
	}
}

func TestGrepResultLineShowsContextOnBothSidesOfMatch(t *testing.T) {
	r := GrepResult{
		RelPath:  "a.go",
		Line:     0,
		Col:      40,
		MatchLen: 5,
		LineText: strings.Repeat("a", 40) + "MATCH" + strings.Repeat("b", 40),
	}
	line := grepResultLine(r, 40)
	if !contains(line, "MATCH") {
		t.Fatalf("expected match to remain visible, got %q", line)
	}
	if !strings.Contains(line, "…a") || !strings.Contains(line, "b…") {
		t.Errorf("expected context truncated with ellipses on both sides, got %q", line)
	}
	if lipgloss.Width(line) > 40 {
		t.Errorf("result line exceeded maxW=40: %q (display width %d)", line, lipgloss.Width(line))
	}
}

// contains is a local spelling of strings.Contains, kept from when these tests
// and the engine's lived in one file.
func contains(s, sub string) bool { return strings.Contains(s, sub) }
