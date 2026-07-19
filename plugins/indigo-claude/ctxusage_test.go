package main

import (
	"strings"
	"testing"
)

func TestCLIModeHeaderShowsNoMisleadingPercentage(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir()) // apiKey="" = CLI mode
	m.width = 100

	updated, _ := m.Update(agentUsageMsg{ctxTokens: 1902900}) // the exact figure from the bug report
	m = updated.(Model)

	got := m.renderHeader()
	if strings.Contains(got, "%") {
		t.Errorf("CLI-mode header shows a percentage (misleading — ctxTokens is cumulative, not context occupancy): %q", got)
	}
	if !strings.Contains(got, "tokens this session") {
		t.Errorf("CLI-mode header missing honest cumulative-tokens framing: %q", got)
	}
}

func TestAPIModeHeaderStillShowsPercentage(t *testing.T) {
	m := newModel(nil, &programLink{}, "sk-test", t.TempDir()) // apiKey set = API mode
	m.width = 100

	updated, _ := m.Update(agentUsageMsg{ctxTokens: 100_000}) // 50% of the 200k window
	m = updated.(Model)

	got := m.renderHeader()
	if !strings.Contains(got, "50%") {
		t.Errorf("API-mode header should still show a context percentage, got: %q", got)
	}
}

func TestCLIModeNeverShowsContextFullWarning(t *testing.T) {
	m := newModel(nil, &programLink{}, "", t.TempDir()) // CLI mode

	// Cumulative tokens for a long CLI session easily blow past the raw
	// contextWindowTokens threshold without meaning anything is actually wrong.
	updated, _ := m.Update(agentUsageMsg{ctxTokens: 1_902_900})
	m = updated.(Model)

	for _, msg := range m.conv {
		if strings.Contains(msg.Content, "Context is") && strings.Contains(msg.Content, "/clear") {
			t.Errorf("CLI mode fired the context-full warning telling the user to /clear, based on a cumulative (not live) token count: %q", msg.Content)
		}
	}
	if m.ctxWarned {
		t.Error("ctxWarned set in CLI mode — warning is API-mode-only")
	}
}

func TestAPIModeStillWarnsOnHighContext(t *testing.T) {
	m := newModel(nil, &programLink{}, "sk-test", t.TempDir()) // API mode

	updated, _ := m.Update(agentUsageMsg{ctxTokens: 170_000}) // 85% > 80% threshold
	m = updated.(Model)

	found := false
	for _, msg := range m.conv {
		if strings.Contains(msg.Content, "Context is") {
			found = true
		}
	}
	if !found {
		t.Error("API mode should still warn when context crosses 80%")
	}
	if !m.ctxWarned {
		t.Error("ctxWarned should be set in API mode after crossing the threshold")
	}
}
