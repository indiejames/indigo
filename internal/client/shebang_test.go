package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/highlight"
)

// TestNewDetectsShebangWhenExtensionUnrecognized is a regression test for a
// live feature request following the reload-loses-override bug fix above:
// an extension-less script (no ":set ft=" override possible before the
// file is even open) should still get correct highlighting by sniffing its
// "#!" shebang line, exactly as if the user had typed ":set ft=sh"
// themselves — including surviving a later external-change reload, since
// it's stored the same way (Model.langOverride).
func TestNewDetectsShebangWhenExtensionUnrecognized(t *testing.T) {
	if highlight.NewForKey("sh") == nil {
		t.Skip("no bash highlighter registered; run with -tags lang_all (or lang_bash)")
	}
	m := New(&RPC{}, 1, "#!/usr/bin/env bash\necho hi\n", 0, "myscript", "/tmp", &config.Config{}, false, 0)

	if m.langOverride != "sh" {
		t.Errorf("langOverride = %q, want %q (detected from shebang)", m.langOverride, "sh")
	}
	if m.hlr == nil {
		t.Error("hlr = nil, want the bash highlighter detected from the shebang")
	}
}

// TestNewLeavesPlainTextWhenNoShebang verifies an extension-less file with
// no shebang line at all still falls back to plain text, same as before
// this feature existed — no false positives.
func TestNewLeavesPlainTextWhenNoShebang(t *testing.T) {
	m := New(&RPC{}, 1, "just some text\nwith no shebang\n", 0, "notes", "/tmp", &config.Config{}, false, 0)

	if m.langOverride != "" {
		t.Errorf("langOverride = %q, want empty (no shebang present)", m.langOverride)
	}
	if m.hlr != nil {
		t.Error("hlr != nil, want plain text when there's no extension and no shebang")
	}
}

// TestNewExtensionTakesPrecedenceOverShebang verifies a file whose
// extension already resolves to a language is never second-guessed by a
// misleading (or coincidentally matching) shebang line.
func TestNewExtensionTakesPrecedenceOverShebang(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	// A real-world pattern: a Python script with a bash-style re-exec
	// shebang trick. The .py extension must win regardless.
	m := New(&RPC{}, 1, "#!/bin/bash\n\"exec\" python3 \"$0\" \"$@\"\n", 0, "script.py", "/tmp", &config.Config{}, false, 0)

	if m.langOverride != "" {
		t.Errorf("langOverride = %q, want empty (extension already resolved, shebang must not override it)", m.langOverride)
	}
	if m.hlr == nil {
		t.Error("hlr = nil, want the Python highlighter derived from the .py extension")
	}
}

// TestSetFtAutoRedetectsShebang verifies ":set ft=auto" on an
// extension-less shebang script re-derives the language from the shebang
// rather than just falling back to plain text — "auto" means fully
// automatic detection, which now includes the shebang fallback.
func TestSetFtAutoRedetectsShebang(t *testing.T) {
	if highlight.NewForKey("sh") == nil || highlight.NewForKey("py") == nil {
		t.Skip("bash/python highlighters not registered; run with -tags lang_all")
	}
	m := newTestModel("#!/usr/bin/env bash\necho hi\n")
	m.filePath = "myscript"
	m.cfg = &config.Config{}
	m.langOverride = "py" // pretend the user had manually forced a different type

	m.cmdBuf = "set ft=auto"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "sh" {
		t.Errorf("langOverride = %q after :set ft=auto, want %q (re-detected from shebang)", got.langOverride, "sh")
	}
	if got.hlr == nil {
		t.Error("hlr = nil after :set ft=auto, want the bash highlighter re-detected from the shebang")
	}
}
