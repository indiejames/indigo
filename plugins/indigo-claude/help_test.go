package main

import (
	"strings"
	"testing"
)

func TestHelpTextListsAllCommands(t *testing.T) {
	text := helpText()
	for _, want := range []string{"/help", "/clear", "/copy", "/model", "/autoapprove"} {
		if !strings.Contains(text, want) {
			t.Errorf("helpText() missing %q:\n%s", want, text)
		}
	}
}

func TestHelpSlashCommand(t *testing.T) {
	for _, cmd := range []string{"/help", "/?"} {
		m := newModel(nil, &programLink{}, "", t.TempDir())
		m = submitText(m, cmd)

		if len(m.input) != 0 {
			t.Errorf("%s: input not cleared: %q", cmd, string(m.input))
		}
		last := m.conv[len(m.conv)-1]
		if last.Role != RoleAssistant {
			t.Errorf("%s: last message role = %v, want RoleAssistant (so it renders multi-line)", cmd, last.Role)
		}
		if !strings.Contains(last.Content, "/autoapprove") {
			t.Errorf("%s: help message missing expected content: %q", cmd, last.Content)
		}
	}
}
