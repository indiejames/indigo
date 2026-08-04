package highlight

import "testing"

func TestNewUnknownExtension(t *testing.T) {
	for _, path := range []string{"file.xyz", "file.unknown", "noextension", ""} {
		if h := New(path); h != nil {
			t.Errorf("New(%q) = non-nil, want nil", path)
		}
	}
}

func TestCaptureANSIFallsBackToMostSpecificEntry(t *testing.T) {
	// "function.method" is a real captureTable entry; the fallback for
	// "function.method.private" (not itself in the table) must land there
	// rather than skipping straight to the more generic "function" entry.
	want, wantPrio, ok := captureANSI("function.method")
	if !ok {
		t.Fatal("captureTable has no \"function.method\" entry — test assumption invalid")
	}
	got, gotPrio, ok := captureANSI("function.method.private")
	if !ok {
		t.Fatal("captureANSI(\"function.method.private\") = not found, want a fallback match")
	}
	if got != want {
		t.Errorf("captureANSI(\"function.method.private\") ansi = %q, want %q (the \"function.method\" color)", got, want)
	}
	if gotPrio != wantPrio-1 {
		t.Errorf("captureANSI(\"function.method.private\") priority = %d, want %d (one below \"function.method\")", gotPrio, wantPrio-1)
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
