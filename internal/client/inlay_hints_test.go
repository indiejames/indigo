package client

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/config"
)

func newInlayTestModel(content string) Model {
	m := newTestModel(content)
	m.cfg = &config.Config{InlayHints: true}
	return m
}

// TestBuildInlayHintOverlaysPositionsAndPads verifies a hint becomes a w:0
// overlay at the right column, with padding applied as literal spaces.
func TestBuildInlayHintOverlaysPositionsAndPads(t *testing.T) {
	m := newInlayTestModel("let x = 1\n")
	m.inlayHints = []ClientInlayHint{
		{Line: 0, Col: 5, Label: ": number", Kind: 1, PaddingLeft: true},
	}
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildInlayHintOverlays(layout, cw)
	if len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatalf("buildInlayHintOverlays() = %v, want one row with one overlay", rows)
	}
	ovl := rows[0][0]
	if ovl.col != 5 {
		t.Errorf("overlay col = %d, want 5", ovl.col)
	}
	if ovl.w != 0 {
		t.Errorf("overlay w = %d, want 0 — an inlay hint must never consume real content", ovl.w)
	}
	if !strings.Contains(ovl.text, " : number") {
		t.Errorf("overlay text = %q, want it to contain the padded label %q", ovl.text, " : number")
	}
}

// TestInlayHintOverlayDoesNotConsumeRealContent is the critical correctness
// property: unlike every other overlay producer in this package (which use w
// to replace real characters — e.g. a search-match highlight covers the
// matched text), an inlay hint is server-inferred virtual text that must never
// hide or eat real buffer content. This drives renderLineRunes directly to
// prove a w:0 overlay renders the hint AND leaves the underlying line intact,
// as opposed to a w>0 overlay (modeled on how other overlays behave), which
// would swallow real characters.
func TestInlayHintOverlayDoesNotConsumeRealContent(t *testing.T) {
	line := []rune("let x = 1")

	var withHint strings.Builder
	renderLineRunes(&withHint, line, -1, -1, -1, nil,
		[]lineOverlay{{col: 5, text: ": number", w: 0}}, nil, nil)
	got := ansi.Strip(withHint.String())
	want := "let x: number = 1"
	if got != want {
		t.Errorf("w:0 overlay: rendered = %q, want %q (hint inserted, no real characters lost)", got, want)
	}

	// Contrast: a w>0 overlay (the semantics every other overlay in this
	// package relies on) DOES eat that many real characters — confirming w:0
	// is the deliberate, correct choice for inlay hints, not an oversight.
	var eating strings.Builder
	renderLineRunes(&eating, line, -1, -1, -1, nil,
		[]lineOverlay{{col: 5, text: "XXXXXXXX", w: 2}}, nil, nil)
	gotEating := ansi.Strip(eating.String())
	wantEating := "let xXXXXXXXX 1" // " =" (2 runes at col 5..7) replaced
	if gotEating != wantEating {
		t.Errorf("w:2 overlay: rendered = %q, want %q (sanity check on overlay semantics)", gotEating, wantEating)
	}
}

// TestBuildInlayHintOverlaysSkipsStaleHintPastEndOfLine mirrors the existing
// search-overlay staleness regression test: a hint computed before an edit
// shortened its line must be skipped, not panic on an out-of-bounds slice.
func TestBuildInlayHintOverlaysSkipsStaleHintPastEndOfLine(t *testing.T) {
	m := newInlayTestModel("short\n")
	m.inlayHints = []ClientInlayHint{{Line: 0, Col: 9, Label: ": number"}}
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	rows := m.buildInlayHintOverlays(layout, cw) // must not panic
	if len(rows) != 1 || len(rows[0]) != 0 {
		t.Errorf("buildInlayHintOverlays() = %v, want one row with no overlays (stale hint skipped)", rows)
	}
}

// TestBuildInlayHintOverlaysRespectsConfigToggle verifies hints are suppressed
// when disabled in config, and when config is nil.
func TestBuildInlayHintOverlaysRespectsConfigToggle(t *testing.T) {
	m := newInlayTestModel("let x = 1\n")
	m.inlayHints = []ClientInlayHint{{Line: 0, Col: 5, Label: ": number"}}
	cw := 80
	layout := m.buildScreenLayout(1, cw)

	m.cfg.InlayHints = false
	if rows := m.buildInlayHintOverlays(layout, cw); rows != nil {
		t.Errorf("expected nil overlays when InlayHints is disabled, got %v", rows)
	}

	m.cfg = nil
	if rows := m.buildInlayHintOverlays(layout, cw); rows != nil {
		t.Errorf("expected nil overlays when cfg is nil, got %v", rows)
	}
}
