package highlight

import "testing"

func TestNewUnknownExtension(t *testing.T) {
	for _, path := range []string{"file.xyz", "file.unknown", "noextension", ""} {
		if h := New(path); h != nil {
			t.Errorf("New(%q) = non-nil, want nil", path)
		}
	}
}

func TestHighlightNilSafe(t *testing.T) {
	var h *Highlighter
	if spans := h.Highlight([]byte("anything")); spans != nil {
		t.Errorf("nil Highlighter.Highlight() = %v, want nil", spans)
	}
}

func TestHighlightEmptyContent(t *testing.T) {
	var h *Highlighter
	if spans := h.Highlight([]byte{}); spans != nil {
		t.Error("nil Highlighter.Highlight(empty) should return nil")
	}
}
