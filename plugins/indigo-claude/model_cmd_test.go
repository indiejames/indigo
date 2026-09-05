package main

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestResolveModel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", defaultModel},
		{"opus", "claude-opus-4-8"},
		{"sonnet", "claude-sonnet-5"},
		{"haiku", "claude-haiku-4-5-20251001"},
		{"fable", "claude-fable-5"},
		{"claude-opus-4-8", "claude-opus-4-8"},           // already a full ID
		{"some-future-model-id", "some-future-model-id"}, // unknown passthrough
	}
	for _, c := range cases {
		if got := resolveModel(c.in); got != c.want {
			t.Errorf("resolveModel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// submitText simulates typing text into the input box and pressing Enter,
// returning the resulting Model.
func submitText(m Model, text string) Model {
	m.input = []rune(text)
	m.inputPos = len(m.input)
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return m2.(Model)
}

func TestModelSlashCommandSetsAndResets(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())

	m = submitText(m, "/model opus")
	if m.model != "opus" {
		t.Fatalf("model = %q, want opus", m.model)
	}
	if last := m.conv[len(m.conv)-1]; !strings.Contains(last.Content, "opus") {
		t.Errorf("status message = %q, want it to mention opus", last.Content)
	}
	if m.modelDisplay() != "opus" {
		t.Errorf("modelDisplay() = %q, want opus", m.modelDisplay())
	}

	m = submitText(m, "/model default")
	if m.model != "" {
		t.Errorf("model = %q, want empty after reset", m.model)
	}

	m = submitText(m, "/model")
	if last := m.conv[len(m.conv)-1]; !strings.Contains(last.Content, "Model:") {
		t.Errorf("bare /model status = %q, want it to start describing current model", last.Content)
	}
}

func TestModelSlashCommandClearsInput(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir())
	m = submitText(m, "/model opus")
	if len(m.input) != 0 {
		t.Errorf("input not cleared after /model: %q", string(m.input))
	}
}
