package client

import (
	"fmt"
	"strconv"
)

// Virtual lines: screen rows that render supplied text instead of buffer
// content.
//
// They exist for the inline git diff, where lines removed relative to HEAD
// have to be shown in place — that content is by definition not in the
// buffer, so there is no buffer line to render it on. A virtual row is
// anchored *above* a buffer line: the removed text appears immediately
// before the line that replaced it, or before the line following a pure
// deletion.
//
// The whole design rests on one property of the layout: every place that
// asks "how many screen rows does buffer line L occupy" goes through
// visualChunks, and there are only four such callers (buildScreenLayout,
// cursorVisualRowFromTop, findTopLineForRow, scrollToShowLineTail). Routing
// all four through rowsForLine below is what keeps virtual rows consistent
// across rendering, cursor positioning and scrolling, rather than each
// having to learn about them separately. Mouse mapping comes along for free
// because it reads the layout (mouse.go) rather than recomputing rows.
//
// Invariants worth preserving:
//   - The cursor never occupies a virtual row (they are not editable
//     positions). screenRowOf must therefore skip them, or a cursor on
//     chunk 0 of an anchor line would match the virtual row sitting above it.
//   - Virtual rows consume viewport space, so scroll math has to count them
//     or a large removal can push the cursor's own line off-screen.

// virtualLineKind selects how a virtual row is styled.
type virtualLineKind int

const (
	// virtualLineRemoved is a line present in HEAD but not in the buffer.
	virtualLineRemoved virtualLineKind = iota
)

// virtualLine is one screen row of supplied text, anchored above a buffer
// line.
type virtualLine struct {
	text string
	kind virtualLineKind
	// emphStart/emphEnd are the rune range within text that differs from the
	// line that replaced it, so an edit inside an otherwise-identical line
	// can be picked out instead of the whole row reading as changed. An
	// empty range (start == end) means no emphasis, which is the right
	// answer for a pure deletion with no counterpart.
	emphStart int
	emphEnd   int
	// oldLine is this content's 1-based line number in the pre-change file,
	// shown in the gutter the way `git diff` numbers a removed line. 0 means
	// unknown, and the gutter number is left blank.
	oldLine int
}

// virtualLinesBefore returns the virtual rows rendered immediately above
// bufLine, in display order. Nil when there are none, which is the common
// case — callers are on the hot rendering path.
func (m Model) virtualLinesBefore(bufLine int) []virtualLine {
	if len(m.virtualLines) == 0 {
		return nil
	}
	return m.virtualLines[bufLine]
}

// rowsForLine returns the number of screen rows buffer line l occupies,
// including any virtual rows anchored above it.
//
// This is the single answer to "how much vertical space does this line take"
// and every layout/scroll/cursor calculation must use it rather than calling
// visualChunks directly, or the four will disagree the moment a diff appears.
func (m Model) rowsForLine(l, cw int) int {
	if l < 0 || l >= m.buf.LineCount() {
		return 1
	}
	runes := []rune(m.buf.Line(l))
	exp, _ := expandTabsRemap(runes)
	return len(m.virtualLinesBefore(l)) + visualChunks(len(exp), cw)
}

// WithVirtualLines replaces the virtual-row set. Passing nil clears it.
//
// The set is expected to change under the layout between frames (the git
// diff that feeds it is recomputed on a debounce), so nothing may cache
// derived row positions across frames.
func (m Model) WithVirtualLines(v map[int][]virtualLine) Model {
	m.virtualLines = v
	return m
}

// tintRange is a background colour applied to a half-open column range
// [StartCol, EndCol) of a line.
//
// Tints are a *separate layer* from highlight.Span, deliberately. Spans
// resolve as "first span covering this column wins" (spanIdxAt in
// renderLineRunes), so expressing a diff tint as a span would make it
// replace syntax highlighting rather than sit behind it. A tint instead
// emits a background SGR before the span's foreground and lets the existing
// reset close both, so syntax colours survive inside a tinted line.
//
// The same mechanism serves both granularities: a whole changed line is a
// tint over [0, len), and an intra-line diff is one or more narrower tints
// marking just the runes that actually differ.
type tintRange struct {
	StartCol int
	EndCol   int
	BG       string // background SGR, e.g. "\x1b[48;2;30;60;30m"

	// ToEOL extends this tint's background across the row's trailing
	// padding, out to the right edge of the window. Whole-line tints
	// (an added or changed line) set it so the row reads as a solid bar the
	// way VS Code's does; an intra-line emphasis span leaves it false so it
	// stops at the runes it marks.
	ToEOL bool
}

// lineTintsFor returns the tints for a buffer line, or nil. Hot path: nil
// for every line in a file with no diff.
func (m Model) lineTintsFor(bufLine int) []tintRange {
	if len(m.lineTints) == 0 {
		return nil
	}
	return m.lineTints[bufLine]
}

// WithLineTints replaces the diff tint set. Passing nil clears it.
func (m Model) WithLineTints(t map[int][]tintRange) Model {
	m.lineTints = t
	return m
}

// bgSGR builds a truecolor background sequence from a "#RRGGBB" string.
// Returns "" for anything malformed, so a bad theme value degrades to "no
// tint" rather than emitting a broken escape into the frame — the same
// defensive posture as highlight.hexToANSI, which does the foreground half.
func bgSGR(hex string) string {
	if len(hex) != 7 || hex[0] != '#' {
		return ""
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", (v>>16)&0xFF, (v>>8)&0xFF, v&0xFF)
}

// rebuildDiffDecorations derives the virtual-row and tint sets from the
// current decoration list.
//
// It runs on every decorations refresh rather than being computed lazily
// during render: the layout, the cursor's screen position and the scroll math
// all consult these sets, and they must agree within a frame. Deriving them
// once when the decorations change is what guarantees that; computing them
// per-caller would let them drift.
//
// Decoration lines are 0-based, like buffer lines (the git plugin converts
// from git's 1-based numbering on its side). Out-of-range values are dropped
// rather than clamped — a stale decoration pointing past the end of a
// shrinking buffer should disappear, not pile up on the last line.
func (m Model) rebuildDiffDecorations() Model {
	var virt map[int][]virtualLine
	var tints map[int][]tintRange
	lineCount := m.buf.LineCount()

	for _, d := range m.decorations {
		line := int(d.Line)
		if line < 0 || line >= lineCount {
			continue
		}
		switch d.Kind {
		case ClientDecorationRemovedLine:
			if virt == nil {
				virt = map[int][]virtualLine{}
			}
			virt[line] = append(virt[line], virtualLine{
				text:      d.Text,
				kind:      virtualLineRemoved,
				emphStart: int(d.Col),
				emphEnd:   int(d.EndCol),
				oldLine:   int(d.OldLine),
			})
		case ClientDecorationLineTint:
			bg := bgSGR(d.TextColor)
			if bg == "" {
				continue // malformed colour: no tint beats a broken escape
			}
			if tints == nil {
				tints = map[int][]tintRange{}
			}
			end := int(d.EndCol)
			lineLen := len([]rune(m.buf.Line(line)))
			tints[line] = append(tints[line], tintRange{
				StartCol: int(d.Col),
				EndCol:   end,
				BG:       bg,
				// A tint covering the whole line reads as a solid bar out to
				// the window edge; a narrower intra-line span stops where it
				// ends.
				ToEOL: int(d.Col) == 0 && end >= lineLen,
			})
		}
	}
	m.virtualLines = virt
	m.lineTints = tints
	// The inline diff is on exactly when the plugin is sending the state that
	// only that mode produces. Deriving it here rather than adding a separate
	// signal keeps the two in lockstep: the gutter can never claim diff mode
	// on a frame where the rows and tints aren't there.
	m.inlineDiffOn = len(virt) > 0 || len(tints) > 0
	return m
}

// gutterAccentAt returns the hex colour a plugin has marked this line with in
// the gutter, or "" if none.
//
// Only consulted while the inline diff is on: outside it the marker is drawn
// as a colour block and the line numbers stay their normal grey, so an edited
// file doesn't sit there permanently tinted during ordinary editing.
func (m Model) gutterAccentAt(bufLine int) string {
	if !m.inlineDiffOn {
		return ""
	}
	d := m.gutterDecorAt(bufLine)
	if d == nil || d.Text == "" {
		return ""
	}
	return d.TextColor
}
