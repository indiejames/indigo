package config

import "testing"

func TestDetectIndentSettingsTabs(t *testing.T) {
	got := DetectIndentSettings("func foo() {\n\tbar()\n\tbaz()\n}\n")
	if got == nil {
		t.Fatal("DetectIndentSettings: got nil, want tabs")
	}
	if got.Style != "tabs" {
		t.Errorf("Style = %q, want %q", got.Style, "tabs")
	}
}

func TestDetectIndentSettingsSpaces(t *testing.T) {
	got := DetectIndentSettings("def foo():\n  bar()\n  baz()\n")
	if got == nil {
		t.Fatal("DetectIndentSettings: got nil, want spaces width 2")
	}
	if got.Style != "spaces" || got.Width != 2 {
		t.Errorf("got %+v, want {spaces 2}", got)
	}
}

func TestDetectIndentSettingsFourSpaces(t *testing.T) {
	got := DetectIndentSettings("def foo():\n    bar()\n    baz()\n")
	if got == nil {
		t.Fatal("DetectIndentSettings: got nil, want spaces width 4")
	}
	if got.Style != "spaces" || got.Width != 4 {
		t.Errorf("got %+v, want {spaces 4}", got)
	}
}

func TestDetectIndentSettingsNoSignal(t *testing.T) {
	if got := DetectIndentSettings("foo\nbar\nbaz\n"); got != nil {
		t.Errorf("DetectIndentSettings: got %+v, want nil (no indented lines)", got)
	}
}

func TestDetectIndentSettingsEmpty(t *testing.T) {
	if got := DetectIndentSettings(""); got != nil {
		t.Errorf("DetectIndentSettings: got %+v, want nil", got)
	}
}

func TestDetectIndentSettingsWhitespaceOnlyLinesIgnored(t *testing.T) {
	// Blank/whitespace-only lines carry no real signal and shouldn't be
	// mistaken for a "spaces" indent.
	got := DetectIndentSettings("foo\n   \nbar\n")
	if got != nil {
		t.Errorf("DetectIndentSettings: got %+v, want nil", got)
	}
}

// TestDetectIndentSettingsIgnoresJSDocCommentLines is a regression test: a
// block comment's " * foo" continuation lines (and the closing " */") have
// exactly 1 leading space regardless of the file's real indent width —
// extremely common in TypeScript/JavaScript/Go/etc via JSDoc/TSDoc/Godoc
// style comments. Without excluding them, minSpaceWidth latched onto 1,
// making every downstream consumer (indent guides in particular) treat the
// file as 1-space-indented instead of its real 2-space indent.
func TestDetectIndentSettingsIgnoresJSDocCommentLines(t *testing.T) {
	src := "/**\n" +
		" * Does a thing.\n" +
		" * @param foo - something\n" +
		" */\n" +
		"function bar(foo) {\n" +
		"  return foo;\n" +
		"}\n"
	got := DetectIndentSettings(src)
	if got == nil {
		t.Fatal("DetectIndentSettings: got nil, want spaces width 2")
	}
	if got.Style != "spaces" || got.Width != 2 {
		t.Errorf("got %+v, want {spaces 2} — a JSDoc comment's 1-space-aligned lines must not skew detection", got)
	}
}
