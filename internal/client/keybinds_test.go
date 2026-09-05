package client

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/config"
)

// resetKeybinds restores prefixCmds/insertCmds to their built-in defaults,
// undoing whatever the test applied. Registered via t.Cleanup so a failed
// assertion never leaks overridden globals into a later test.
func resetKeybinds(t *testing.T) {
	t.Cleanup(func() { applyKeybindOverrides(nil) })
}

func TestApplyKeybindOverridesRebindsExistingKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "w", Action: "cursor-up"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 1
	m2, _ := m.handleNormal(fakeKey("w"))
	got := m2.(Model)
	if got.cursor.Line != 0 {
		t.Errorf("w rebound to cursor-up: cursor.Line = %d, want 0", got.cursor.Line)
	}
}

func TestApplyKeybindOverridesAddsNewKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "select-all"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m2, _ := m.handleNormal(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.sel == nil {
		t.Error("ctrl+g bound to select-all: sel should be set")
	}
}

func TestApplyKeybindOverridesUnknownAction(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "does-not-exist"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown action") {
		t.Errorf("warnings = %v, want one containing 'unknown action'", warnings)
	}
}

func TestApplyKeybindOverridesUnknownMode(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "weird", Key: "ctrl+g", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unknown mode") {
		t.Errorf("warnings = %v, want one containing 'unknown mode'", warnings)
	}
}

func TestApplyKeybindOverridesEmptyKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "empty key") {
		t.Errorf("warnings = %v, want one containing 'empty key'", warnings)
	}
}

func TestApplyKeybindOverridesRefusesPrefixMenuKey(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "g", Action: "select-all"},
	}}
	warnings := applyKeybindOverrides(cfg)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "prefix menu") {
		t.Errorf("warnings = %v, want one containing 'prefix menu'", warnings)
	}
	cmd, ok := findCommand([]string{"g"})
	if !ok || len(cmd.children) == 0 {
		t.Error("g menu should be untouched after a refused override")
	}
}

func TestApplyKeybindOverridesRevertsOnReload(t *testing.T) {
	resetKeybinds(t)
	applyKeybindOverrides(&config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "0", Action: "cursor-up"},
	}})
	// Simulate a config reload that drops the override.
	applyKeybindOverrides(&config.Config{})

	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 1
	m.cursor.Col = 3
	m2, _ := m.handleNormal(fakeKey("0"))
	got := m2.(Model)
	// go-to-line-start (the default) resets Col to 0 without touching Line;
	// cursor-up (the dropped override) would instead move Line to 0 and
	// leave Col at 3 — these are unambiguous, distinguishable outcomes.
	if got.cursor.Line != 1 || got.cursor.Col != 0 {
		t.Errorf("after reload without override: cursor = %+v, want {Line:1 Col:0} (back to go-to-line-start)", got.cursor)
	}
}

func TestApplyKeybindOverridesInsertMode(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "insert", Key: "ctrl+g", Action: "line-start"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	m.mode = ModeInsert
	m.cursor.Col = 3
	m2, _ := m.handleInsert(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.cursor.Col != 0 {
		t.Errorf("ctrl+g bound to line-start: cursor.Col = %d, want 0", got.cursor.Col)
	}
}

// fnPointer returns a comparable identity for an action's execute function,
// so two command nodes can be checked for pointing at the literal same
// implementation rather than just having equal names.
func fnPointer(fn func(Model) (tea.Model, tea.Cmd)) uintptr {
	return reflect.ValueOf(fn).Pointer()
}

// TestNoDuplicateActionNames guards the invariant the rest of the
// unification work depends on: a name must never resolve to two different
// execute functions. Names are now load-bearing for both [[keybind]]
// overrides and ":"-command resolution (see ex_actions.go), so a
// copy-pasted name landing on the wrong action would be a real bug, not a
// style nit.
func TestNoDuplicateActionNames(t *testing.T) {
	check := func(t *testing.T, label string, cmds []command) {
		seen := map[string]uintptr{}
		var walk func([]command)
		walk = func(cs []command) {
			for _, c := range cs {
				if c.name != "" && c.execute != nil {
					p := fnPointer(c.execute)
					if prev, ok := seen[c.name]; ok && prev != p {
						t.Errorf("%s: name %q is bound to two different execute functions", label, c.name)
					}
					seen[c.name] = p
				}
				if len(c.children) > 0 {
					walk(c.children)
				}
			}
		}
		walk(cmds)
	}
	check(t, "prefixCmds", defaultPrefixCmds)
	check(t, "insertCmds", defaultInsertCmds)

	treeNames := actionRegistry(defaultPrefixCmds)
	for name := range exOnlyActions {
		if _, exists := treeNames[name]; exists {
			t.Errorf("exOnlyActions[%q] collides with an existing keypress-tree action name", name)
		}
	}
}

// TestNoDuplicateActionLabels guards the invariant the generated help popup
// depends on: every occurrence of a given name across the built-in trees
// must agree on its label. This can't be enforced for arbitrary
// [[keybind]] config at runtime (that's what rebindRoot's canonical-label
// lookup is for instead), but our own static tree has no excuse — a
// mismatch here (found and fixed this session: "open-file-picker" and
// "go-to-symbol-in-project" each had two different labels) would make a
// merged-by-name popup row show an arbitrary, unpredictable label.
func TestNoDuplicateActionLabels(t *testing.T) {
	check := func(t *testing.T, treeLabel string, cmds []command) {
		seen := map[string]string{}
		var walk func([]command)
		walk = func(cs []command) {
			for _, c := range cs {
				if c.name != "" && c.execute != nil {
					if prev, ok := seen[c.name]; ok && prev != c.label {
						t.Errorf("%s: name %q has two different labels: %q and %q", treeLabel, c.name, prev, c.label)
					}
					seen[c.name] = c.label
				}
				if len(c.children) > 0 {
					walk(c.children)
				}
			}
		}
		walk(cmds)
	}
	check(t, "prefixCmds", defaultPrefixCmds)
	check(t, "insertCmds", defaultInsertCmds)
}

// TestRebindRootUsesCanonicalLabel is the regression test for the bug found
// this session: rebindRoot used to stamp the bare action name (e.g.
// "go-to-top") as label, rather than looking up the action's real display
// label — every [[keybind]] a user ever writes hit this, not just an edge
// case.
func TestRebindRootUsesCanonicalLabel(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "go-to-top"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	cmd, ok := findCommand([]string{"ctrl+g"})
	if !ok {
		t.Fatal("ctrl+g not found after override")
	}
	if cmd.label != "Go to top of file" {
		t.Errorf("label = %q, want canonical label %q (not the bare action name)", cmd.label, "Go to top of file")
	}
	if cmd.category != "Go to (g)" {
		t.Errorf("category = %q, want %q", cmd.category, "Go to (g)")
	}
}

// TestApplyKeybindOverridesReachesFormerlyMenuOnlyAction proves that naming
// a menu leaf (here, "go-to-top", previously reachable only via "g g") is
// enough on its own to make it bindable to a bare key via config — no
// override-mechanism changes needed.
func TestApplyKeybindOverridesReachesFormerlyMenuOnlyAction(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+g", Action: "go-to-top"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("a\nb\nc\n")
	m.cursor.Line = 2
	m2, _ := m.handleNormal(fakeKey("ctrl+g"))
	got := m2.(Model)
	if got.cursor.Line != 0 {
		t.Errorf("ctrl+g bound to go-to-top: cursor.Line = %d, want 0", got.cursor.Line)
	}
}

// TestApplyKeybindOverridesExOnlyAction proves an action with no keypress
// or menu equivalent at all (exOnlyActions) is a valid [[keybind]] target,
// once merged into applyKeybindOverrides' action maps.
func TestApplyKeybindOverridesExOnlyAction(t *testing.T) {
	resetKeybinds(t)
	cfg := &config.Config{Keybinds: []config.Keybind{
		{Mode: "normal", Key: "ctrl+q", Action: "quit-force"},
	}}
	if warnings := applyKeybindOverrides(cfg); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	m := newTestModel("hello\n")
	_, cmd := m.handleNormal(fakeKey("ctrl+q"))
	if cmd == nil {
		t.Error("ctrl+q bound to quit-force: expected a non-nil cmd (doCloseBuffer)")
	}
}

// TestExActionRegistryMatchesTreeAction confirms exActionRegistry's "save"
// entry is literally the same function ctrl+s uses — the concrete
// unification win: no second, independently-maintained implementation.
func TestExActionRegistryMatchesTreeAction(t *testing.T) {
	resetKeybinds(t)
	treeFn, ok := actionRegistry(prefixCmds)["save"]
	if !ok {
		t.Fatal(`"save" not found in the keypress tree's action registry`)
	}
	exFn, ok := exActionRegistry()["save"]
	if !ok {
		t.Fatal(`"save" not found in exActionRegistry`)
	}
	if fnPointer(treeFn) != fnPointer(exFn) {
		t.Error("exActionRegistry()[\"save\"] is not the same function ctrl+s uses")
	}
}
