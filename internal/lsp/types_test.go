package lsp

import (
	"encoding/json"
	"testing"
)

func TestHoverTextMarkupContent(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"contents": map[string]any{"kind": "markdown", "value": "**hello**"},
	})
	var h Hover
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	if got := h.Text(); got != "**hello**" {
		t.Errorf("got %q, want %q", got, "**hello**")
	}
}

func TestHoverTextPlainString(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"contents": "plain hover text",
	})
	var h Hover
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	if got := h.Text(); got != "plain hover text" {
		t.Errorf("got %q, want %q", got, "plain hover text")
	}
}

func TestHoverTextMarkedStringArray(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"contents": []any{
			map[string]any{"language": "typescript", "value": "function foo(): void"},
			"Documentation for foo.",
		},
	})
	var h Hover
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatal(err)
	}
	got := h.Text()
	if got != "function foo(): void\n\nDocumentation for foo." {
		t.Errorf("got %q", got)
	}
}

func TestHoverTextNil(t *testing.T) {
	var h *Hover
	if got := h.Text(); got != "" {
		t.Errorf("nil hover should return empty string, got %q", got)
	}
}
