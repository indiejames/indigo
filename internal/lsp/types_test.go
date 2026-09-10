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

// TestFormattingOptionsMarshalsExtraFlat covers how server-specific settings
// reach the server. LSP defines the formatting options object as an open map,
// and servers (typescript-language-server among them) read their own keys
// straight off it — so an Extra key has to appear as a sibling of tabSize, not
// nested under one. Nesting it would be silently ignored: the request stays
// valid, the setting just never takes effect.
func TestFormattingOptionsMarshalsExtraFlat(t *testing.T) {
	opts := FormattingOptions{TabSize: 2, InsertSpaces: true, Extra: map[string]any{
		"insertSpaceAfterFunctionKeywordForAnonymousFunctions": true,
	}}
	data, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["insertSpaceAfterFunctionKeywordForAnonymousFunctions"] != true {
		t.Errorf("marshalled %s; the extra setting must sit alongside tabSize", data)
	}
	if got["tabSize"] != float64(2) || got["insertSpaces"] != true {
		t.Errorf("marshalled %s; the standard options must survive", data)
	}
}

// TestFormattingOptionsExtraCannotShadowStandardOptions: tabSize and
// insertSpaces are required by the protocol, so a stray Extra key of the same
// name must lose rather than corrupt the request.
func TestFormattingOptionsExtraCannotShadowStandardOptions(t *testing.T) {
	opts := FormattingOptions{TabSize: 4, InsertSpaces: false, Extra: map[string]any{
		"tabSize": "not a number", "insertSpaces": "nope",
	}}
	data, err := json.Marshal(opts)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got["tabSize"] != float64(4) || got["insertSpaces"] != false {
		t.Errorf("marshalled %s, want the struct's own tabSize/insertSpaces to win", data)
	}
}
