package client

import "testing"

func TestDetectIndentSettingsTabs(t *testing.T) {
	got := detectIndentSettings("func foo() {\n\tbar()\n\tbaz()\n}\n")
	if got == nil {
		t.Fatal("detectIndentSettings: got nil, want tabs")
	}
	if got.Style != "tabs" {
		t.Errorf("Style = %q, want %q", got.Style, "tabs")
	}
}

func TestDetectIndentSettingsSpaces(t *testing.T) {
	got := detectIndentSettings("def foo():\n  bar()\n  baz()\n")
	if got == nil {
		t.Fatal("detectIndentSettings: got nil, want spaces width 2")
	}
	if got.Style != "spaces" || got.Width != 2 {
		t.Errorf("got %+v, want {spaces 2}", got)
	}
}

func TestDetectIndentSettingsFourSpaces(t *testing.T) {
	got := detectIndentSettings("def foo():\n    bar()\n    baz()\n")
	if got == nil {
		t.Fatal("detectIndentSettings: got nil, want spaces width 4")
	}
	if got.Style != "spaces" || got.Width != 4 {
		t.Errorf("got %+v, want {spaces 4}", got)
	}
}

func TestDetectIndentSettingsNoSignal(t *testing.T) {
	if got := detectIndentSettings("foo\nbar\nbaz\n"); got != nil {
		t.Errorf("detectIndentSettings: got %+v, want nil (no indented lines)", got)
	}
}

func TestDetectIndentSettingsEmpty(t *testing.T) {
	if got := detectIndentSettings(""); got != nil {
		t.Errorf("detectIndentSettings: got %+v, want nil", got)
	}
}

func TestDetectIndentSettingsWhitespaceOnlyLinesIgnored(t *testing.T) {
	// Blank/whitespace-only lines carry no real signal and shouldn't be
	// mistaken for a "spaces" indent.
	got := detectIndentSettings("foo\n   \nbar\n")
	if got != nil {
		t.Errorf("detectIndentSettings: got %+v, want nil", got)
	}
}
