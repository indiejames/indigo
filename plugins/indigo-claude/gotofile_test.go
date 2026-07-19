package main

import "testing"

func TestGotoFileWireLine(t *testing.T) {
	cases := []struct {
		oneBased int
		want     uint32
	}{
		{0, 0},
		{-1, 0},
		{1, 0},
		{2, 1},
		{42, 41},
	}
	for _, c := range cases {
		if got := gotoFileWireLine(c.oneBased); got != c.want {
			t.Errorf("gotoFileWireLine(%d) = %d, want %d", c.oneBased, got, c.want)
		}
	}
}

func TestGotoFileToolRegistered(t *testing.T) {
	found := false
	for _, tool := range allTools() {
		if tool.Name == "goto_file" {
			found = true
			if len(tool.InputSchema.Required) != 1 || tool.InputSchema.Required[0] != "path" {
				t.Errorf("goto_file required fields = %v, want [path]", tool.InputSchema.Required)
			}
			if _, ok := tool.InputSchema.Properties["line"]; !ok {
				t.Error("goto_file schema missing optional 'line' property")
			}
		}
	}
	if !found {
		t.Error("goto_file not found in allTools()")
	}
}

func TestGotoFileExposedToCLIMode(t *testing.T) {
	found := false
	for _, tool := range mcpTools() {
		if tool.Name == "goto_file" {
			found = true
		}
	}
	if !found {
		t.Error("goto_file not exposed via mcpTools() — CLI mode (claude -p) won't have it")
	}
}
