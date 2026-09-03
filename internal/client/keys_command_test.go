package client

import (
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/highlight"
)

func TestParseGrepArgs(t *testing.T) {
	cases := []struct {
		in                        string
		pattern, include, exclude string
	}{
		{"TODO", "TODO", "", ""},
		{"TODO *.go", "TODO", "*.go", ""},
		{"TODO !vendor/", "TODO", "", "vendor/"},
		{"TODO *.go !vendor/", "TODO", "*.go", "vendor/"},
		{"TODO *.go *.ts !vendor/ !**/*_test.go", "TODO", "*.go *.ts", "vendor/ **/*_test.go"},
		{"foo bar", "foo bar", "", ""},
		{"*.go", "", "*.go", ""},
		{"!vendor/", "", "", "vendor/"},
		{"", "", "", ""},
	}
	for _, c := range cases {
		pattern, include, exclude := parseGrepArgs(c.in)
		if pattern != c.pattern || include != c.include || exclude != c.exclude {
			t.Errorf("parseGrepArgs(%q) = (%q, %q, %q), want (%q, %q, %q)",
				c.in, pattern, include, exclude, c.pattern, c.include, c.exclude)
		}
	}
}

// TestExecuteCommandSetFileType is an end-to-end regression test for
// ":set ft=<lang>": it must immediately swap the highlighter AND every
// other file-type-derived thing (indent settings, comment prefix, status
// bar label) over to the requested language — not just syntax highlighting
// — even though filePath itself (and its real extension) never changes.
func TestExecuteCommandSetFileType(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.filePath = "notes.txt" // an extension Python's own settings clearly differ from
	m.cfg = &config.Config{}

	m.cmdBuf = "set ft=py"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", got.mode)
	}
	if got.langOverride != "py" {
		t.Errorf("langOverride = %q, want %q", got.langOverride, "py")
	}
	if got.hlr == nil {
		t.Fatal("hlr = nil, want the Python highlighter")
	}
	if !strings.Contains(got.status, "Python") {
		t.Errorf("status = %q, want it to mention Python", got.status)
	}
	if name := got.effectiveFileTypeName(); name != "Python" {
		t.Errorf("effectiveFileTypeName() = %q, want %q", name, "Python")
	}
	if prefix := got.lineCommentPrefix(); prefix != "#" {
		t.Errorf("lineCommentPrefix() = %q, want %q (Python's, not notes.txt's default)", prefix, "#")
	}
	wantIndent := config.IndentSettings{Style: "spaces", Width: 4}
	if indent := got.effectiveIndentSettings(); indent != wantIndent {
		t.Errorf("effectiveIndentSettings() = %+v, want %+v (Python's, not notes.txt's default)", indent, wantIndent)
	}
}

// TestExecuteCommandSetFileTypeCaseInsensitive verifies ":set ft=PY" resolves
// the same as ":set ft=py".
func TestExecuteCommandSetFileTypeCaseInsensitive(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.cmdBuf = "set ft=PY"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "py" {
		t.Errorf("langOverride = %q, want %q", got.langOverride, "py")
	}
	if got.hlr == nil {
		t.Error("hlr = nil, want the Python highlighter")
	}
}

// TestExecuteCommandSetFileTypeUnknown verifies an unrecognized language
// leaves the buffer's existing highlighter/override untouched and reports
// an error instead of silently clearing highlighting.
func TestExecuteCommandSetFileTypeUnknown(t *testing.T) {
	m := newTestModel("")
	m.filePath = "test.go"
	m.hlr = highlight.New("test.go")
	m.cmdBuf = "set ft=not-a-real-language"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "" {
		t.Errorf("langOverride = %q, want empty (unknown language must not set an override)", got.langOverride)
	}
	if got.hlr != m.hlr {
		t.Error("hlr changed on an unknown file type, want it left untouched")
	}
	if !strings.Contains(got.status, "unknown file type") {
		t.Errorf("status = %q, want it to report the unknown file type", got.status)
	}
}

// TestExecuteCommandSaveViaAlias verifies ":save" resolves through the
// unified action registry (ex_actions.go) rather than falling through to
// "unknown command" — the ":" half of the save/ctrl+s unification proof
// (see TestExActionRegistryMatchesTreeAction for the "same function" half).
func TestExecuteCommandSaveViaAlias(t *testing.T) {
	m := newTestModel("hello\n")
	m.cmdBuf = "save"
	m2, cmd := m.executeCommand()
	got := m2.(Model)
	if got.mode != ModeNormal {
		t.Errorf("mode = %v, want ModeNormal", got.mode)
	}
	if strings.Contains(got.status, "unknown command") {
		t.Errorf("status = %q, want no unknown-command error", got.status)
	}
	if cmd == nil {
		t.Error(`":save" should return a non-nil cmd (doSave)`)
	}
}

// TestExecuteCommandQuitDirtyGuard pins down the highest-risk regression in
// the dispatch-unification refactor: ":q"/":quit" on a dirty buffer must
// still refuse and report the error, not silently close (that behavior is
// what force-quit, ":q!", exists for).
func TestExecuteCommandQuitDirtyGuard(t *testing.T) {
	for _, cmdText := range []string{"q", "quit"} {
		m := newTestModel("hello\n")
		m.buf.MarkDirty()
		m.cmdBuf = cmdText
		m2, cmd := m.executeCommand()
		got := m2.(Model)
		if !strings.Contains(got.status, "unsaved changes") {
			t.Errorf(":%s on dirty buffer: status = %q, want it to mention unsaved changes", cmdText, got.status)
		}
		if got.mode != ModeNormal {
			t.Errorf(":%s on dirty buffer: mode = %v, want ModeNormal", cmdText, got.mode)
		}
		if cmd != nil {
			t.Errorf(":%s on dirty buffer: cmd = %v, want nil (buffer must not close)", cmdText, cmd)
		}
	}
}

// TestExecuteCommandAllZeroArgAliasesRecognized is the regression net for
// exCommandAliases itself: every alias must resolve to a real registered
// action, catching a typo made while transcribing the old switch's case
// labels into the map.
func TestExecuteCommandAllZeroArgAliasesRecognized(t *testing.T) {
	reg := exActionRegistry()
	for alias, name := range exCommandAliases {
		if _, ok := reg[name]; !ok {
			t.Errorf("exCommandAliases[%q] = %q, which is not registered in exActionRegistry()", alias, name)
		}
	}
}

// TestExecuteCommandAliasesPinTrickyPairs directly pins the pairs where a
// transcription mistake would silently change behavior rather than error:
// "q!"/"quit!" must map to the force variant, not the dirty-checked one.
func TestExecuteCommandAliasesPinTrickyPairs(t *testing.T) {
	cases := map[string]string{
		"q":     "quit",
		"quit":  "quit",
		"q!":    "quit-force",
		"quit!": "quit-force",
	}
	for alias, want := range cases {
		if got := exCommandAliases[alias]; got != want {
			t.Errorf("exCommandAliases[%q] = %q, want %q", alias, got, want)
		}
	}
}

// TestExecuteCommandSaveSurvivesCtrlSRebind is a regression test:
// rebindRoot overrides a key by overwriting that tree node's name/execute
// in place, so rebinding ctrl+s away from "save" (a single-location action
// — no other node carries that name) used to erase "save" from the live
// tree entirely, breaking ":save" purely as a side effect of an unrelated
// key remap. exActionRegistry resolves against the immutable
// defaultPrefixCmds specifically to prevent this.
func TestExecuteCommandSaveSurvivesCtrlSRebind(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+s", Action: "select-all"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m.cmdBuf = "save"
	m2, cmd := m.executeCommand()
	got := m2.(Model)
	if strings.Contains(got.status, "unknown command") {
		t.Errorf(`":save" after ctrl+s rebind: status = %q, want no unknown-command error`, got.status)
	}
	if cmd == nil {
		t.Error(`":save" after ctrl+s rebind: expected a non-nil cmd (doSave)`)
	}
}

// TestExecuteCommandSetFileTypeAuto verifies ":set ft=auto" clears a
// previously set override and reverts to filePath-derived language.
func TestExecuteCommandSetFileTypeAuto(t *testing.T) {
	if highlight.NewForKey("py") == nil {
		t.Skip("no Python highlighter registered; run with -tags lang_all (or lang_py)")
	}
	m := newTestModel("")
	m.filePath = "test.go"
	m.langOverride = "py"
	m.hlr = highlight.NewForKey("py")

	m.cmdBuf = "set ft=auto"
	m2, _ := m.executeCommand()
	got := m2.(Model)

	if got.langOverride != "" {
		t.Errorf("langOverride = %q, want empty after :set ft=auto", got.langOverride)
	}
	if name := got.effectiveFileTypeName(); name != "Go" {
		t.Errorf("effectiveFileTypeName() = %q, want %q (back to filePath's own type)", name, "Go")
	}
}

// TestWithLangOverrideSurvivesReload is a regression test for a live bug
// report: setting ":set ft=sh" on an extension-less shell script correctly
// switches to bash highlighting, but an external-change reload
// (app.doReloadBuffer) rebuilds the Model from scratch via New, which
// derives the language from filePath's extension alone and has no way to
// know about a prior override — silently reverting to plain text. New's
// caller must carry LangOverride() across to WithLangOverride on the fresh
// Model, exactly as doReloadBuffer now does.
func TestWithLangOverrideSurvivesReload(t *testing.T) {
	if highlight.NewForKey("sh") == nil {
		t.Skip("no bash highlighter registered; run with -tags lang_all (or lang_bash)")
	}
	m := newTestModel("")
	m.filePath = "myscript" // no recognizable extension, matching the bug report
	m.cfg = &config.Config{}

	m.cmdBuf = "set ft=sh"
	m2, _ := m.executeCommand()
	before := m2.(Model)
	if before.langOverride != "sh" {
		t.Fatalf("langOverride = %q, want %q", before.langOverride, "sh")
	}

	// Simulate what New(filePath) alone produces on reload — the language
	// override is lost since it derives purely from the (unrecognized)
	// extension.
	reloaded := New(&RPC{}, before.bufID, "", 0, before.filePath, before.workDir, before.cfg, false, 0)
	if reloaded.langOverride != "" || reloaded.hlr != nil {
		t.Fatalf("New() unexpectedly preserved the override on its own — test assumption is wrong")
	}

	// doReloadBuffer must reapply it via LangOverride/WithLangOverride.
	restored := reloaded.WithLangOverride(before.LangOverride())
	if restored.langOverride != "sh" {
		t.Errorf("langOverride = %q after WithLangOverride, want %q", restored.langOverride, "sh")
	}
	if restored.hlr == nil {
		t.Error("hlr = nil after WithLangOverride, want the bash highlighter restored")
	}
}

// TestWithLangOverrideNoOpWhenUnset verifies WithLangOverride leaves a
// freshly constructed Model untouched when there was nothing to restore
// (the common case: no prior ":set ft=" override).
func TestWithLangOverrideNoOpWhenUnset(t *testing.T) {
	m := newTestModel("")
	m.filePath = "test.go"
	orig := m.hlr

	got := m.WithLangOverride("")
	if got.langOverride != "" {
		t.Errorf("langOverride = %q, want empty", got.langOverride)
	}
	if got.hlr != orig {
		t.Error("hlr changed on a no-op WithLangOverride(\"\")")
	}
}
