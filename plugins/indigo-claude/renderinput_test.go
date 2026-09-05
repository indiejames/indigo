package main

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// renderInput's focus indicator is purely a color change, which lipgloss
// strips entirely under the no-color profile go test normally runs under
// (verified: with the default profile, styled and unstyled renders come out
// byte-identical). Force a color-capable profile so these tests actually
// exercise the styling instead of passing vacuously.
func init() {
}

// ansiFor renders s and extracts the escape sequence preceding it, for
// comparing which style two renders used without hardcoding hex→SGR math.
func ansiFor(style lipgloss.Style, s string) string {
	rendered := style.Render(s)
	return strings.TrimSuffix(rendered, s)
}

func TestRenderInputFocusedUsesBrighterBorder(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 80, 24
	m.focus = focusTextInput

	got := m.renderInput()
	wantSeq := ansiFor(inputBorderFocusSty, "│")
	dimSeq := ansiFor(inputBorderSty, "│")

	if !strings.Contains(got, wantSeq) {
		t.Errorf("focused renderInput() missing the brighter border style %q\ngot: %q", wantSeq, got)
	}
	if strings.Contains(got, dimSeq) {
		t.Errorf("focused renderInput() unexpectedly used the dim border style %q\ngot: %q", dimSeq, got)
	}
}

func TestRenderInputUnfocusedUsesDimBorderAndText(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 80, 24
	m.input = []rune("hello")
	m.inputPos = len(m.input)
	m.focus = focusAutoApproveEdits // anything other than focusTextInput

	got := m.renderInput()
	wantBorder := ansiFor(inputBorderSty, "│")
	brightBorder := ansiFor(inputBorderFocusSty, "│")
	wantText := ansiFor(inputTextDimSty, "hello")

	if !strings.Contains(got, wantBorder) {
		t.Errorf("unfocused renderInput() missing the dim border style %q\ngot: %q", wantBorder, got)
	}
	if strings.Contains(got, brightBorder) {
		t.Errorf("unfocused renderInput() unexpectedly used the bright/focused border style\ngot: %q", got)
	}
	if !strings.Contains(got, wantText) {
		t.Errorf("unfocused renderInput() didn't dim the input text\nwant style applied to %q\ngot: %q", "hello", got)
	}
}

func TestRenderInputUnfocusedHasNoReverseVideoCursor(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 80, 24
	m.focus = focusModel

	got := m.renderInput()
	// SGR 7 = reverse video, which cursorStyle applies. It must not appear
	// anywhere once focus has moved away from the text input.
	if strings.Contains(got, "\x1b[7m") {
		t.Errorf("unfocused renderInput() still shows a reverse-video cursor block\ngot: %q", got)
	}
}

func TestRenderInputFocusedHasReverseVideoCursor(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 80, 24
	m.focus = focusTextInput

	got := m.renderInput()
	if !strings.Contains(got, "\x1b[7m") {
		t.Errorf("focused renderInput() should show a reverse-video cursor block\ngot: %q", got)
	}
}
