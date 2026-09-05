package client

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
	"github.com/indiejames/indigo/internal/theme"
)

// --- renderLineRunes ---

func TestRenderLineRunesPlain(t *testing.T) {
	var sb strings.Builder
	runes := []rune("hello")
	renderLineRunes(&sb, runes, -1, -1, -1, nil, nil, nil)
	if got := sb.String(); got != "hello" {
		t.Errorf("plain = %q, want %q", got, "hello")
	}
}

func TestRenderLineRunesCursorAtEnd(t *testing.T) {
	var sb strings.Builder
	runes := []rune("hi")
	// curCol == len(runes): cursor rendered as trailing space
	renderLineRunes(&sb, runes, -1, -1, 2, nil, nil, nil)
	got := sb.String()
	if got == "" {
		t.Error("cursor beyond line should append styled space")
	}
}

func TestRenderLineRunesEmpty(t *testing.T) {
	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, -1, -1, -1, nil, nil, nil)
	if sb.String() != "" {
		t.Errorf("empty runes: got %q, want empty", sb.String())
	}
}

// TestRenderLineRunesNestedSpanWinsOverEnclosingSpan is a regression test
// for renderLineRunes' half of the nested-capture rendering fix (see
// internal/highlight's sortSpansByNesting, which is responsible for putting
// spans in this winning-first order — this test locks down the other half
// of the contract: given spans already in that order, spanIdxAt's "first
// span covering this column wins" must actually surface the narrower,
// nested span at their overlap rather than the wider one, on a line shaped
// like a real gitcommit hint ("# On branch main", where a wide @comment
// span covers the whole line and a narrower @markup.link span nested
// inside it covers just "main") plus a third span disjoint from both
// (mirroring a separate hint elsewhere on the same physical line).
func TestRenderLineRunesNestedSpanWinsOverEnclosingSpan(t *testing.T) {
	const (
		outerANSI    = "\x1b[38;2;1;1;1m"
		nestedANSI   = "\x1b[38;2;2;2;2m"
		disjointANSI = "\x1b[38;2;3;3;3m"
	)
	line := []rune("# On branch main HEAD")
	//              0123456789012345678901
	//                        1111111111222
	spans := []highlight.Span{
		// Nested span must come first: it's the winner extractSpans already
		// resolved for the overlap with the outer span below.
		{StartCol: 12, EndCol: 16, ANSI: nestedANSI},   // "main"
		{StartCol: 0, EndCol: 17, ANSI: outerANSI},     // "# On branch main" (the enclosing comment)
		{StartCol: 17, EndCol: 21, ANSI: disjointANSI}, // "HEAD" — disjoint from both
	}

	var sb strings.Builder
	renderLineRunes(&sb, line, -1, -1, -1, spans, nil, nil)
	got := sb.String()

	nestedText := outerANSI + "main"
	if strings.Contains(got, nestedText) {
		t.Errorf("rendered = %q: \"main\" was styled with the outer span's ANSI (%q) instead of the nested span's (%q)", got, outerANSI, nestedANSI)
	}
	if !strings.Contains(got, nestedANSI+"main") {
		t.Errorf("rendered = %q, want it to contain %q (nested span wins over its enclosing span)", got, nestedANSI+"main")
	}
	if !strings.Contains(got, disjointANSI+"HEAD") {
		t.Errorf("rendered = %q, want it to contain %q (disjoint span renders independently of the nested/outer pair)", got, disjointANSI+"HEAD")
	}
	if plain := ansi.Strip(got); plain != string(line) {
		t.Errorf("stripped rendered text = %q, want %q (no characters lost)", plain, string(line))
	}
}

// TestRenderLineRunesTrailingOverlaysAreSpacedByColumn covers a blank line
// (no real characters) carrying two overlays past end-of-content — the case
// for two indent guides on a blank line inside a nested block. The trailing
// overlay-drain loop used to concatenate overlay text with no regard for
// .col, collapsing the gap between them to zero instead of padding with
// spaces up to each overlay's column.
func TestRenderLineRunesTrailingOverlaysAreSpacedByColumn(t *testing.T) {
	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, -1, -1, -1, nil, []lineOverlay{
		{col: 0, text: "|", w: 1},
		{col: 2, text: "|", w: 1},
	}, nil)
	if got, want := sb.String(), "| |"; got != want {
		t.Errorf("trailing overlays = %q, want %q", got, want)
	}
}

// TestRenderLineRunesTrailingOverlayColumnWithCursorOnEmptyLine is a
// regression test: on an empty line, curCol >= n is always true (0 >= 0), so
// the cursor's placeholder cell is written to sb before any trailing
// overlays are processed. That write must advance i (the column bookkeeping
// the trailing-overlay padding loop relies on) or every trailing overlay
// after it — e.g. a ruler column past the end of the line — renders one
// column to the right of where it belongs.
func TestRenderLineRunesTrailingOverlayColumnWithCursorOnEmptyLine(t *testing.T) {
	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, -1, -1, 0, nil, []lineOverlay{
		{col: 2, text: "|", w: 1},
	}, nil)
	// cursor cell (col 0) + 1 col of padding (col 1) + the overlay at col 2.
	if got, want := ansi.Strip(sb.String()), "  |"; got != want {
		t.Errorf("trailing overlay with cursor on empty line = %q, want %q", got, want)
	}
}

// TestRenderLineRunesOverlayInSelectionUsesSelectionStyle verifies an overlay
// with a plain glyph set (e.g. an indent guide) renders with selectionStyle,
// not its own fixed style, when its column falls inside the active
// selection — otherwise it visually stands out as unselected against the
// highlighted text around it.
func TestRenderLineRunesOverlayInSelectionUsesSelectionStyle(t *testing.T) {
	guideOverlay := lineOverlay{col: 0, text: indentGuideStyle.Render("▏"), w: 1, plain: "▏"}

	var selected strings.Builder
	renderLineRunes(&selected, []rune("  x"), 0, 2, -1, nil, []lineOverlay{guideOverlay}, nil)
	// Assert the styling of the glyph rather than exact span boundaries:
	// adjacent runes sharing a style may be emitted as one styled span or
	// several, which is a rendering detail that says nothing about whether
	// the overlay picked up selectionStyle.
	got := selected.String()
	if !strings.Contains(got, selectionStyle.Render("▏")) {
		t.Errorf("overlay glyph inside selection should render with selectionStyle, got %q", got)
	}
	if strings.Contains(got, guideOverlay.text) {
		t.Errorf("overlay inside selection kept its own indentGuideStyle instead of selectionStyle: %q", got)
	}

	var unselected strings.Builder
	renderLineRunes(&unselected, []rune("  x"), -1, -1, -1, nil, []lineOverlay{guideOverlay}, nil)
	if got := unselected.String(); !strings.Contains(got, guideOverlay.text) {
		t.Errorf("overlay outside selection = %q, want it to keep its own style %q", got, guideOverlay.text)
	}
}

// TestRenderLineRunesTrailingOverlayInSelectionUsesSelectionStyle covers the
// same rule for the past-end-of-content path (a blank, selected line): the
// overlay at column 0 and the padding before a later overlay must also pick
// up selectionStyle when inside [selA, selB].
func TestRenderLineRunesTrailingOverlayInSelectionUsesSelectionStyle(t *testing.T) {
	guide := lineOverlay{col: 0, text: indentGuideStyle.Render("▏"), w: 1, plain: "▏"}

	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, 0, 0, -1, nil, []lineOverlay{guide}, nil)
	if got, want := sb.String(), selectionStyle.Render("▏"); got != want {
		t.Errorf("blank selected line overlay = %q, want %q", got, want)
	}
}

// TestRenderLineRunesTrailingOverlayPaddingSplitsAtSelectionBoundary covers a
// blank line where the selection ends partway through the gap before a later
// overlay: only the padding cells inside [selA, selB] should pick up
// selectionStyle, the rest of the gap must stay unselected instead of the
// whole span (keyed off just the gap's start column) rendering as selected.
func TestRenderLineRunesTrailingOverlayPaddingSplitsAtSelectionBoundary(t *testing.T) {
	// Render() is a no-op without a color profile (as in a normal `go test`
	// run with no tty), which would make the old all-or-nothing styling and
	// the fixed split-at-boundary styling produce identical plain-text
	// output. Force real ANSI output so the two are actually distinguishable.

	guide := lineOverlay{col: 4, text: indentGuideStyle.Render("▏"), w: 1, plain: "▏"}

	var sb strings.Builder
	renderLineRunes(&sb, []rune{}, 0, 1, -1, nil, []lineOverlay{guide}, nil)
	want := selectionStyle.Render("  ") + "  " + guide.text
	if got := sb.String(); got != want {
		t.Errorf("blank selected line padding before overlay = %q, want %q", got, want)
	}
}

func TestRenderLineChunkTrailingEmptyLineReverseSelection(t *testing.T) {
	// "a\nb\n" has a trailing phantom empty line (index 2) that isn't part
	// of displayLineCount. Select from line 0 through that phantom line,
	// then simulate a flipped/reverse selection where the cursor sits back
	// on line 0 — the phantom line is still selected but no longer holds
	// the cursor, so it must fall through to the selection-padding branch
	// instead of the plain "~" shortcut.
	m := newTestModel("a\nb\n")
	m.sel = &Selection{
		Anchor: document.Pos{Line: 2, Col: 0},
		Head:   document.Pos{Line: 0, Col: 0},
	}
	m.cursor = document.Pos{Line: 0, Col: 0}

	cw := 80
	layout := m.buildScreenLayout(3, cw)
	rendered := m.renderLineChunk(layout[2], cw, nil, -1, -1, false)

	if strings.Contains(rendered, "~") {
		t.Errorf("trailing empty line included in selection should not render as a bare tilde, got %q", rendered)
	}
}

func TestRenderLineChunkTrailingEmptyLineUnselectedStillTilde(t *testing.T) {
	// Same buffer, no selection: the phantom line should still render as a
	// plain tilde (preserves existing behavior for the common case).
	m := newTestModel("a\nb\n")

	cw := 80
	layout := m.buildScreenLayout(3, cw)
	rendered := m.renderLineChunk(layout[2], cw, nil, -1, -1, false)

	if !strings.Contains(rendered, "~") {
		t.Errorf("unselected trailing empty line should render as a tilde, got %q", rendered)
	}
}

func TestRenderLineRunesEmptyLineSelected(t *testing.T) {
	var sb strings.Builder
	// Empty line covered by a selection (selA=0, selB=0, no cursor here):
	// should still render a styled padding cell, not nothing, so the line
	// visibly shows as selected.
	renderLineRunes(&sb, []rune{}, 0, 0, -1, nil, nil, nil)
	if sb.String() == "" {
		t.Error("empty selected line should render a styled space, got empty output")
	}
}

// --- overlayRight ---

func TestOverlayRight(t *testing.T) {
	main := strings.Repeat("a", 20)
	popup := "POPUP"
	result := overlayRight(main, popup, 10)
	if !strings.HasSuffix(result, "POPUP") {
		t.Errorf("overlayRight: result does not end with popup: %q", result)
	}
	w := lipgloss.Width(result)
	if w != 15 { // 10 chars + 5 popup chars
		t.Errorf("overlayRight width = %d, want 15", w)
	}
}

func TestOverlayRightPadsShortMain(t *testing.T) {
	main := "ab"
	popup := "POP"
	// popCol=10, main is only 2 chars — should be padded to 10 then popup appended
	result := overlayRight(main, popup, 10)
	w := lipgloss.Width(result)
	if w != 13 {
		t.Errorf("overlayRight padded: width = %d, want 13", w)
	}
}

// --- renderPopupBox ---

func TestRenderPopupBoxBasic(t *testing.T) {
	items := []command{
		{key: "s", label: "Save"},
		{key: "q", label: "Quit"},
	}
	lines := renderPopupBox("Menu", items, 40)
	if len(lines) < 4 { // top + 2 items + bottom
		t.Errorf("renderPopupBox: got %d lines, want >= 4", len(lines))
	}
}

func TestRenderPopupBoxEmpty(t *testing.T) {
	lines := renderPopupBox("Empty", nil, 40)
	if len(lines) != 2 { // just top + bottom border
		t.Errorf("empty items: got %d lines, want 2", len(lines))
	}
}

func TestRenderPopupBoxLabelTruncation(t *testing.T) {
	items := []command{
		{key: "x", label: strings.Repeat("A", 100)},
	}
	// Should not panic on very narrow maxW
	lines := renderPopupBox("T", items, 8)
	_ = lines
}

// --- renderMetricsBox ---

func TestRenderMetricsBox(t *testing.T) {
	m := &metricsData{
		renderDuration:     2 * time.Millisecond,
		highlightDuration:  500 * time.Microsecond,
		keyToFrameDuration: 10 * time.Millisecond,
	}
	lines := renderMetricsBox(m)
	if len(lines) != 5 { // top + 3 rows + bottom
		t.Errorf("renderMetricsBox: got %d lines, want 5", len(lines))
	}
}

// --- renderLine ---

func TestRenderLineNormal(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	line := m.renderLine(0, nil)
	if lipgloss.Width(line) != m.width {
		t.Errorf("renderLine width = %d, want %d", lipgloss.Width(line), m.width)
	}
	// Strip ANSI: the cursor cell styles the first character on its own, so
	// the styled string has an SGR reset sitting inside the word.
	if stripped := ansi.Strip(line); !strings.Contains(stripped, "hello") {
		t.Errorf("renderLine should contain 'hello', got: %q (stripped: %q)", line, stripped)
	}
}

// TestViewSelectsCursorStyleByMode verifies View() picks insertCursorStyle
// while in insert mode and normalCursorStyle otherwise, so the buffer cursor
// visibly changes color as a mode indicator distinct from the status bar.
func TestViewSelectsCursorStyleByMode(t *testing.T) {
	m := newTestModel("hello\n")

	m.mode = ModeInsert
	m.View()
	if cursorStyle.GetBackground() != insertCursorStyle.GetBackground() {
		t.Errorf("insert mode: cursorStyle background = %v, want insertCursorStyle's %v",
			cursorStyle.GetBackground(), insertCursorStyle.GetBackground())
	}

	m.mode = ModeNormal
	m.View()
	if cursorStyle.GetBackground() != normalCursorStyle.GetBackground() {
		t.Errorf("normal mode: cursorStyle background = %v, want normalCursorStyle's %v",
			cursorStyle.GetBackground(), normalCursorStyle.GetBackground())
	}
}

// TestApplyThemeUsesInsertCursorBgFromTheme verifies ApplyTheme drives both
// the insert-mode cursor and its status bar label from theme.UI.InsertCursorBg,
// so a custom theme's color choice actually takes effect instead of the
// pre-theme-load fallback constant winning.
func TestApplyThemeUsesInsertCursorBgFromTheme(t *testing.T) {
	defer applyDefaultDark() // restore package-level style vars for later tests

	th := &theme.Theme{
		UI: theme.UI{
			BarBg:          "#000000",
			InsertCursorBg: "#FF00FF",
		},
	}
	ApplyTheme(th)

	if got := insertCursorStyle.GetBackground(); got != lipgloss.Color("#FF00FF") {
		t.Errorf("insertCursorStyle background = %v, want theme's InsertCursorBg #FF00FF", got)
	}
	if got := insertModeStyle.GetForeground(); got != lipgloss.Color("#FF00FF") {
		t.Errorf("insertModeStyle foreground = %v, want theme's InsertCursorBg #FF00FF", got)
	}
}

func TestRenderLineTildeForEmptyRows(t *testing.T) {
	m := newTestModel("hello\n")
	m.height = 24
	// Row beyond buffer content should show tilde
	line := m.renderLine(5, nil)
	if !strings.Contains(line, "~") {
		t.Errorf("out-of-buffer row should contain '~', got: %q", line)
	}
}

func TestRenderLineWithTabs(t *testing.T) {
	m := newTestModel("\thello\n")
	line := m.renderLine(0, nil)
	// Tab should expand to spaces
	if strings.Contains(line, "\t") {
		t.Error("renderLine should expand tabs, but raw tab found")
	}
}

// --- renderStatusBar ---

func TestRenderStatusBarNormalMode(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	bar := m.renderStatusBar()
	w := lipgloss.Width(bar)
	if w != m.width {
		t.Errorf("statusBar width = %d, want %d", w, m.width)
	}
	// Should contain mode label
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "NORMAL") {
		t.Errorf("normal mode bar should contain 'NORMAL': %q", stripped)
	}
}

func TestRenderStatusBarInsertMode(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "INSERT") {
		t.Errorf("insert mode bar should contain 'INSERT': %q", stripped)
	}
}

func TestRenderStatusBarCommandMode(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "quit"
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, ":quit") {
		t.Errorf("command mode bar should contain ':quit': %q", stripped)
	}
}

func TestRenderStatusBarDirtyFlag(t *testing.T) {
	m := newTestModel("hello\n")
	m.filePath = "test.go"
	m.buf.Apply(document.Op{Type: document.OpInsert, InsertLine: 0, InsertCol: 5, InsertText: "!"})
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "[+]") {
		t.Errorf("dirty buffer bar should contain '[+]': %q", stripped)
	}
}

func TestRenderStatusBarColumnDefaultsToViewColumn(t *testing.T) {
	// A tab at col 0 occupies 4 visual columns by default, so the cursor
	// sitting right after it is buffer-column 1 but view-column 5.
	m := newTestModel("\tx\n")
	m.filePath = "test.go"
	m.cursor.Col = 1
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "1:5") {
		t.Errorf("nil-config bar should default to view column 1:5, got: %q", stripped)
	}
}

func TestRenderStatusBarColumnBufferStyle(t *testing.T) {
	m := newTestModel("\tx\n")
	m.filePath = "test.go"
	m.cfg = &config.Config{CursorColumnStyle: "buffer"}
	m.cursor.Col = 1
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "1:2") {
		t.Errorf("buffer-style bar should show raw rune column 1:2, got: %q", stripped)
	}
}

func TestRenderStatusBarColumnViewStyleExplicit(t *testing.T) {
	m := newTestModel("\tx\n")
	m.filePath = "test.go"
	m.cfg = &config.Config{CursorColumnStyle: "view"}
	m.cursor.Col = 1
	bar := m.renderStatusBar()
	stripped := ansiStrip(bar)
	if !strings.Contains(stripped, "1:5") {
		t.Errorf("view-style bar should show visual column 1:5, got: %q", stripped)
	}
}

func TestRenderStatusBarZeroWidth(t *testing.T) {
	m := newTestModel("")
	m.width = 0
	bar := m.renderStatusBar()
	if bar != "" {
		t.Errorf("zero-width bar should be empty, got %q", bar)
	}
}

// --- View ---

func TestViewZeroWidth(t *testing.T) {
	m := newTestModel("hello\n")
	m.width = 0
	if got := m.View().Content; got != "loading…" {
		t.Errorf("View() with zero width = %q, want 'loading…'", got)
	}
}

func TestViewNormal(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	out := m.View().Content
	if out == "" {
		t.Error("View() should return non-empty output")
	}
	stripped := ansiStrip(out)
	if !strings.Contains(stripped, "hello") {
		t.Errorf("View() should contain 'hello': %q", stripped[:min(len(stripped), 200)])
	}
}

func TestViewCommandModeShowsPopup(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeCommand
	m.cmdBuf = ""
	out := m.View().Content
	stripped := ansiStrip(out)
	// The popup title "Commands" should appear
	if !strings.Contains(stripped, "Commands") {
		t.Errorf("command mode View should contain popup 'Commands': %q", stripped[:min(len(stripped), 400)])
	}
}

// --- handleKey dispatcher ---

func TestHandleKeyDispatchesToNormal(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeNormal
	m2, _ := m.handleKey(fakeKey("down"))
	got := m2.(Model)
	if got.cursor.Line != 1 {
		t.Errorf("handleKey normal 'down': cursor.Line = %d, want 1", got.cursor.Line)
	}
}

func TestHandleKeyDispatchesToInsert(t *testing.T) {
	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 2
	m2, _ := m.handleKey(fakeKey("esc"))
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("handleKey insert 'esc': mode = %v, want ModeNormal", got.mode)
	}
}

func TestHandleKeyDispatchesToCommand(t *testing.T) {
	m := newTestModel("")
	m.mode = ModeCommand
	m.cmdBuf = "qui"
	m2, _ := m.handleKey(fakeKey("t"))
	got := m2.(Model)
	if got.cmdBuf != "quit" {
		t.Errorf("handleKey command 't': cmdBuf = %q, want 'quit'", got.cmdBuf)
	}
}

func TestHandleKeyClearsStatus(t *testing.T) {
	m := newTestModel("")
	m.status = "some error"
	m.mode = ModeNormal
	m2, _ := m.handleKey(fakeKey("esc"))
	got := m2.(Model)
	if got.status != "" {
		t.Errorf("handleKey should clear status, got %q", got.status)
	}
}

// ansiStrip removes ANSI escape sequences for plain-text assertions.
func ansiStrip(s string) string {
	var out strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case inEsc:
			// still consuming escape
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// TestCursorShapeConfigSelectsUnderline verifies the cursor_shape config
// option switches the buffer cursor between the default block (reverse
// video / filled cell) and an underline, in both modes — and that picking a
// shape doesn't cost the per-mode color distinction, which is separate
// modal feedback.
func TestCursorShapeConfigSelectsUnderline(t *testing.T) {
	m := newTestModel("hello\n")

	t.Run("default is block", func(t *testing.T) {
		m.cfg = &config.Config{} // no cursor_shape set
		m.mode = ModeNormal
		if got := m.cursorStyleForMode(); !got.GetReverse() {
			t.Error("normal mode with unset cursor_shape: want the reverse-video block cursor")
		}
		if got := m.cursorStyleForMode(); got.GetUnderline() {
			t.Error("normal mode with unset cursor_shape: cursor must not be underlined")
		}
	})

	t.Run("unrecognized value falls back to block", func(t *testing.T) {
		m.cfg = &config.Config{CursorShape: "banana"}
		m.mode = ModeNormal
		if got := m.cursorStyleForMode(); !got.GetReverse() || got.GetUnderline() {
			t.Error(`cursor_shape="banana": want the block cursor, matching cursor_column_style's fallback behavior`)
		}
	})

	t.Run("underline in normal mode", func(t *testing.T) {
		m.cfg = &config.Config{CursorShape: "underline"}
		m.mode = ModeNormal
		got := m.cursorStyleForMode()
		if !got.GetUnderline() {
			t.Error("normal mode: want an underlined cursor")
		}
		if got.GetReverse() {
			t.Error("normal mode: underline cursor must not also be reverse-video (that's the block shape)")
		}
	})

	t.Run("underline in insert mode keeps the mode color", func(t *testing.T) {
		m.cfg = &config.Config{CursorShape: "underline"}
		m.mode = ModeInsert
		insert := m.cursorStyleForMode()
		if !insert.GetUnderline() {
			t.Error("insert mode: want an underlined cursor")
		}
		m.mode = ModeNormal
		normal := m.cursorStyleForMode()
		if insert.GetForeground() == normal.GetForeground() {
			t.Errorf("insert and normal underline cursors share foreground %v; the shape must not "+
				"collapse the per-mode color distinction", insert.GetForeground())
		}
	})
}

// TestBufferCursorIsNotPaintedInNormalMode is the counterpart to the cursor
// tests in cursor_test.go: the shape now reaches the screen through the
// terminal's own cursor (View.Cursor), so the frame must NOT also contain a
// painted cursor cell. Two cursors would otherwise be visible at once.
//
// This replaced an earlier test that asserted the painted cell *was* present
// — correct for the pre-v2 implementation, where restyling a cell was the
// only cursor indigo could draw.
func TestBufferCursorIsNotPaintedInNormalMode(t *testing.T) {
	applyDefaultDark()
	t.Cleanup(applyDefaultDark)

	// Cursor starts at line 0, col 0, so the painted cell would be "hello"'s "h".
	blockCell := normalCursorStyle.Render("h")
	underlinedCell := normalCursorUnderlineStyle.Render("h")

	for _, shape := range []string{"block", "underline", "bar"} {
		m := newTestModel("hello\n")
		m.width, m.height = 40, 6
		m.mode = ModeNormal
		m.cfg = &config.Config{CursorShape: shape}

		out := m.View().Content
		if strings.Contains(out, blockCell) || strings.Contains(out, underlinedCell) {
			t.Errorf("cursor_shape=%s: frame still paints a cursor cell; the terminal cursor "+
				"already draws it, so this shows two cursors at once:\n%q", shape, out)
		}
	}
}
