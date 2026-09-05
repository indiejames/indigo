package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestNextModelChoiceCyclesAndWraps(t *testing.T) {
	// Order is "" (default), then modelAliasOrder.
	want := append([]string{""}, modelAliasOrder...)

	cur := ""
	for i := 0; i < len(want); i++ {
		if cur != want[i] {
			t.Fatalf("step %d: cur = %q, want %q", i, cur, want[i])
		}
		cur = nextModelChoice(cur, 1)
	}
	if cur != want[0] {
		t.Errorf("after a full forward cycle, cur = %q, want wrap to %q", cur, want[0])
	}

	// Backward from "" should land on the last entry.
	if got := nextModelChoice("", -1); got != want[len(want)-1] {
		t.Errorf("nextModelChoice(\"\", -1) = %q, want %q", got, want[len(want)-1])
	}
}

func TestNextModelChoiceUnknownValueStartsFromDefault(t *testing.T) {
	// A full model ID set via /model isn't in the cycle list; stepping
	// forward from it should behave as if starting from "default".
	got := nextModelChoice("claude-opus-4-8-20260101", 1)
	want := nextModelChoice("", 1)
	if got != want {
		t.Errorf("nextModelChoice(unknown, 1) = %q, want %q (same as from default)", got, want)
	}
}

// pressKey drives msg through Update and returns the resulting Model.
func pressKey(m Model, msg tea.KeyMsg) Model {
	m2, _ := m.Update(msg)
	return m2.(Model)
}

func TestTabEntersControlBarEvenWhileAgentRunning(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.agentRunning = true // the case that matters: mid-turn

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})

	if m.focus != focusAutoApproveEdits {
		t.Fatalf("focus = %v, want focusAutoApproveEdits, even while agentRunning", m.focus)
	}
}

func TestTabCyclesThroughAllFociAndWrapsToTextInput(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())

	want := []inputFocus{focusAutoApproveEdits, focusAutoApproveShell, focusModel, focusTextInput}
	for i, w := range want {
		m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab})
		if m.focus != w {
			t.Fatalf("after %d Tabs: focus = %v, want %v", i+1, m.focus, w)
		}
	}
}

func TestShiftTabFromTextInputWrapsToLastField(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})

	if m.focus != focusModel {
		t.Errorf("focus after shift+tab from text input = %v, want focusModel (wrap backward)", m.focus)
	}
}

func TestEscFromBarFieldReturnsFocusToTextInput(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.focus = focusAutoApproveShell

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEscape})

	if m.focus != focusTextInput {
		t.Errorf("focus after Esc = %v, want focusTextInput", m.focus)
	}
}

func TestControlBarTogglesAutoApproveEdits(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.focus = focusAutoApproveEdits

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	edits, shell := m.prog.autoApprove()
	if !edits {
		t.Error("Enter on focusAutoApproveEdits did not turn auto-approve edits on")
	}
	if shell {
		t.Error("focusAutoApproveEdits unexpectedly affected auto-approve shell")
	}

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeySpace})
	edits, _ = m.prog.autoApprove()
	if edits {
		t.Error("second activation did not toggle auto-approve edits back off")
	}
}

func TestControlBarTogglesAutoApproveShell(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.focus = focusAutoApproveShell

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	_, shell := m.prog.autoApprove()
	if !shell {
		t.Error("Right on focusAutoApproveShell did not turn auto-approve shell on")
	}
}

func TestControlBarModelFieldCyclesWithLeftRight(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.focus = focusModel

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyRight})
	if m.model != modelAliasOrder[0] {
		t.Errorf("model after one Right = %q, want %q", m.model, modelAliasOrder[0])
	}

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.model != "" {
		t.Errorf("model after Left back = %q, want \"\" (default)", m.model)
	}
}

// TestControlBarKeysDoNotReachTextInput is a regression test: keys consumed
// while a bar field has focus (e.g. Enter) must not also fall through and
// mutate the conversation/input, the way they would if focus routing broke.
func TestControlBarKeysDoNotReachTextInput(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.input = []rune("hello")
	m.inputPos = len(m.input)
	m.focus = focusAutoApproveEdits
	convLenBefore := len(m.conv)

	m = pressKey(m, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(m.conv) != convLenBefore {
		t.Errorf("conv grew from %d to %d — Enter leaked through to message submission", convLenBefore, len(m.conv))
	}
	if string(m.input) != "hello" {
		t.Errorf("input = %q, want untouched \"hello\"", string(m.input))
	}
}

func TestCtrlCStillCancelsAgentWhileBarFieldFocused(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.focus = focusModel
	m.agentRunning = true

	m = pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	if m.agentRunning {
		t.Error("Ctrl+C did not cancel the running agent while a control-bar field had focus")
	}
}

func TestRenderControlBarShowsFieldsAndHintWhenRoomAllows(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 120, 40
	m.model = "opus"

	bar := m.renderControlBar()
	for _, want := range []string{"Auto-approve:", "Edits:", "Shell:", "Model:", "opus", "Tab:"} {
		if !strings.Contains(bar, want) {
			t.Errorf("renderControlBar() missing %q\ngot: %q", want, bar)
		}
	}
}

// TestRenderControlBarKeepsLabelButDropsHintAtMediumWidth is a regression
// test for the three-tier degrade: the "Auto-approve:" grouping label
// (added so first-time users know Edits/Shell are about auto-approval)
// should survive on terminals too narrow for the Tab hint but wide enough
// for the label itself.
func TestRenderControlBarKeepsLabelButDropsHintAtMediumWidth(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 80, 40

	bar := m.renderControlBar()
	if !strings.Contains(bar, "Auto-approve:") {
		t.Errorf("renderControlBar() dropped the grouping label at medium width: %q", bar)
	}
	if strings.Contains(bar, "cycle focus") {
		t.Errorf("renderControlBar() kept the hint at medium width: %q", bar)
	}
}

func TestRenderControlBarDropsLabelAndHintWhenNarrow(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 40, 40

	bar := m.renderControlBar()
	if strings.Contains(bar, "cycle focus") {
		t.Errorf("renderControlBar() kept the hint on a narrow terminal: %q", bar)
	}
	if strings.Contains(bar, "Auto-approve:") {
		t.Errorf("renderControlBar() kept the grouping label on a narrow terminal: %q", bar)
	}
	if !strings.Contains(bar, "Edits:") {
		t.Errorf("renderControlBar() dropped the fields themselves: %q", bar)
	}
}

func TestRenderControlBarUniformWidth(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 100, 40

	bar := m.renderControlBar()
	if got := lipgloss.Width(bar); got != m.width {
		t.Errorf("renderControlBar() width = %d, want %d", got, m.width)
	}
}
