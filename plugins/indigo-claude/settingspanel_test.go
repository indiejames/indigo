package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestTabOpensSettingsPanelEvenWhileAgentRunning(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.agentRunning = true // the case that matters: mid-turn

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyTab})

	if m.settingsPanel == nil {
		t.Fatal("Tab did not open the settings panel while agentRunning")
	}
	if m.settingsPanel.focus != 0 {
		t.Errorf("focus = %d, want 0", m.settingsPanel.focus)
	}
}

func TestSettingsPanelTabCyclesFocusWithWrap(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.settingsPanel = &settingsPanelState{}

	for i := 1; i < settingsPanelRowCount; i++ {
		m = pressKey(m, tea.KeyMsg{Type: tea.KeyTab})
		if m.settingsPanel.focus != i {
			t.Fatalf("after %d Tabs: focus = %d, want %d", i, m.settingsPanel.focus, i)
		}
	}
	// One more Tab should wrap back to 0.
	m = pressKey(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.settingsPanel.focus != 0 {
		t.Errorf("focus after wrap = %d, want 0", m.settingsPanel.focus)
	}

	// Shift+Tab from 0 should wrap to the last row.
	m = pressKey(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.settingsPanel.focus != settingsPanelRowCount-1 {
		t.Errorf("focus after shift+tab wrap = %d, want %d", m.settingsPanel.focus, settingsPanelRowCount-1)
	}
}

func TestSettingsPanelEscCloses(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.settingsPanel = &settingsPanelState{}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyEsc})

	if m.settingsPanel != nil {
		t.Error("Esc did not close the settings panel")
	}
}

func TestSettingsPanelTogglesAutoApproveEdits(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.settingsPanel = &settingsPanelState{focus: 0}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyEnter})
	edits, shell := m.prog.autoApprove()
	if !edits {
		t.Error("Enter on row 0 did not turn auto-approve edits on")
	}
	if shell {
		t.Error("row 0 unexpectedly affected auto-approve shell")
	}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeySpace})
	edits, _ = m.prog.autoApprove()
	if edits {
		t.Error("second activation did not toggle auto-approve edits back off")
	}
}

func TestSettingsPanelTogglesAutoApproveShell(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.settingsPanel = &settingsPanelState{focus: 1}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyRight})
	_, shell := m.prog.autoApprove()
	if !shell {
		t.Error("Right on row 1 did not turn auto-approve shell on")
	}
}

func TestSettingsPanelModelRowCyclesWithLeftRight(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.settingsPanel = &settingsPanelState{focus: 2}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.model != modelAliasOrder[0] {
		t.Errorf("model after one Right = %q, want %q", m.model, modelAliasOrder[0])
	}

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.model != "" {
		t.Errorf("model after Left back = %q, want \"\" (default)", m.model)
	}
}

// TestSettingsPanelKeysDoNotReachTextInput is a regression test: keys
// consumed by the open settings panel (e.g. Enter) must not also fall
// through and mutate the conversation/input, the way they would if the
// panel weren't checked first in handleKey.
func TestSettingsPanelKeysDoNotReachTextInput(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.input = []rune("hello")
	m.inputPos = len(m.input)
	m.settingsPanel = &settingsPanelState{focus: 0}
	convLenBefore := len(m.conv)

	m = pressKey(m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.conv) != convLenBefore {
		t.Errorf("conv grew from %d to %d — Enter leaked through to message submission", convLenBefore, len(m.conv))
	}
	if string(m.input) != "hello" {
		t.Errorf("input = %q, want untouched \"hello\"", string(m.input))
	}
}

func TestBuildSettingsPopupShowsFocusedRow(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m.width, m.height = 100, 40
	m.settingsPanel = &settingsPanelState{focus: 2}
	m.model = "opus"

	lines := m.buildSettingsPopup()
	if len(lines) == 0 {
		t.Fatal("buildSettingsPopup() returned no lines")
	}
	want := lipgloss.Width(lines[0])
	for i, l := range lines {
		if got := lipgloss.Width(l); got != want {
			t.Errorf("line %d width = %d, want %d (uniform border)\nline: %q", i, got, want, l)
		}
	}
}
