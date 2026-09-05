package main

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestRestoreStateStagesAutoApproveWithoutApplyingIt(t *testing.T) {
	m := newModel(nil, &programLink{}, "", "/w")
	st := &persistedState{
		Version:          stateVersion,
		WorkDir:          "/w",
		Mode:             "cli",
		Conv:             []ConvMsg{{Role: RoleUser, Content: "hi"}},
		AutoApproveEdits: true,
		AutoApproveShell: true,
	}

	m = m.restoreState(st)

	if m.pendingAutoApproveRestore == nil {
		t.Fatal("restoreState did not stage a pending auto-approve restore")
	}
	if !m.pendingAutoApproveRestore.edits || !m.pendingAutoApproveRestore.shell {
		t.Errorf("pendingAutoApproveRestore = %+v, want both true", m.pendingAutoApproveRestore)
	}
	// Must not be applied yet — that's the whole point of the confirmation.
	edits, shell := m.prog.autoApprove()
	if edits || shell {
		t.Errorf("restoreState applied auto-approve before confirmation: edits=%v shell=%v", edits, shell)
	}
}

func TestRestoreStateNoPendingWhenAutoApproveWasOff(t *testing.T) {
	m := newModel(nil, &programLink{}, "", "/w")
	st := &persistedState{
		Version: stateVersion,
		WorkDir: "/w",
		Mode:    "cli",
		Conv:    []ConvMsg{{Role: RoleUser, Content: "hi"}},
	}

	m = m.restoreState(st)

	if m.pendingAutoApproveRestore != nil {
		t.Errorf("pendingAutoApproveRestore = %+v, want nil when nothing was on", m.pendingAutoApproveRestore)
	}
}

func TestAutoApproveRestoreConfirmApplies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	m := newModel(nil, &programLink{}, "", "/w")
	m.pendingAutoApproveRestore = &autoApproveRestoreState{edits: true, shell: false}

	// Move selection to "Yes" then confirm.
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = m2.(Model)
	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(Model)

	if m.pendingAutoApproveRestore != nil {
		t.Error("pendingAutoApproveRestore not cleared after confirmation")
	}
	edits, shell := m.prog.autoApprove()
	if !edits || shell {
		t.Errorf("after confirming restore: edits=%v shell=%v, want edits=true shell=false", edits, shell)
	}
	if cmd == nil {
		t.Fatal("confirming restore did not return a save command")
	}
	cmd() // run the save synchronously

	got := loadState("/w", "")
	if got == nil || !got.AutoApproveEdits {
		t.Errorf("persisted state after confirming restore = %+v, want AutoApproveEdits=true", got)
	}
}

func TestAutoApproveRestoreDeclineDoesNotApplyAndPersistsOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	// Seed a state file the way a previous session (with auto-approve on)
	// would have left it, so we can confirm declining overwrites it.
	seed := persistedState{
		Version:          stateVersion,
		WorkDir:          "/w",
		Mode:             "cli",
		Conv:             []ConvMsg{{Role: RoleUser, Content: "hi"}},
		AutoApproveEdits: true,
		AutoApproveShell: true,
	}
	if err := writeState(seed); err != nil {
		t.Fatal(err)
	}

	m := newModel(nil, &programLink{}, "", "/w")
	m.pendingAutoApproveRestore = &autoApproveRestoreState{edits: true, shell: true}

	// Default choice is "No" — bare Enter should decline.
	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m2.(Model)

	if m.pendingAutoApproveRestore != nil {
		t.Error("pendingAutoApproveRestore not cleared after decline")
	}
	edits, shell := m.prog.autoApprove()
	if edits || shell {
		t.Errorf("declining restore applied auto-approve anyway: edits=%v shell=%v", edits, shell)
	}
	if cmd == nil {
		t.Fatal("declining restore did not return a save command")
	}
	cmd()

	got := loadState("/w", "")
	if got == nil {
		t.Fatal("state file missing after decline")
	}
	if got.AutoApproveEdits || got.AutoApproveShell {
		t.Errorf("declining restore left stale auto-approve=true in the state file: %+v", got)
	}
}

// TestAutoApproveRestoreModalBlocksOtherKeys is a regression test: while the
// modal is pending, keys must not leak through to focus-cycling or message
// submission, the same way the permission popup already blocks everything.
func TestAutoApproveRestoreModalBlocksOtherKeys(t *testing.T) {
	m := newModel(nil, &programLink{}, "", "/w")
	m.pendingAutoApproveRestore = &autoApproveRestoreState{edits: true}
	convLenBefore := len(m.conv)

	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = m2.(Model)
	if m.focus != focusTextInput {
		t.Errorf("Tab changed focus while the restore modal was pending: focus = %v", m.focus)
	}
	if m.pendingAutoApproveRestore == nil {
		t.Error("Tab dismissed the pending restore modal")
	}

	m.input = []rune("hello")
	m.inputPos = len(m.input)
	m2, _ = m.Update(tea.KeyPressMsg{Text: string([]rune("x")), Code: []rune(string([]rune("x")))[0]})
	m = m2.(Model)
	if string(m.input) != "hello" {
		t.Errorf("typing leaked through to the text input while the restore modal was pending: %q", string(m.input))
	}
	if len(m.conv) != convLenBefore {
		t.Error("conv grew while the restore modal was pending")
	}
}

func TestSnapshotStateCapturesLiveAutoApprove(t *testing.T) {
	m := newModel(nil, &programLink{}, "", "/w")
	m.prog.setAutoApproveEdits(true)

	st := snapshotState(m)

	if !st.AutoApproveEdits || st.AutoApproveShell {
		t.Errorf("snapshotState AutoApproveEdits=%v AutoApproveShell=%v, want true/false", st.AutoApproveEdits, st.AutoApproveShell)
	}
}

func TestAdjustFocusedControlPersistsAutoApproveToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")

	m := newModel(nil, &programLink{}, "", "/w")
	m.focus = focusAutoApproveEdits

	_, cmd := m.adjustFocusedControl(1)
	if cmd == nil {
		t.Fatal("toggling auto-approve edits from the control bar did not return a save command")
	}
	cmd()

	got := loadState("/w", "")
	if got == nil || !got.AutoApproveEdits {
		t.Errorf("persisted state after toggling = %+v, want AutoApproveEdits=true", got)
	}
}
