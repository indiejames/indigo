package highlight

import "testing"

func TestNewUnknownExtension(t *testing.T) {
	for _, path := range []string{"file.xyz", "file.unknown", "noextension", ""} {
		if h := New(path); h != nil {
			t.Errorf("New(%q) = non-nil, want nil", path)
		}
	}
}

// TestHexToANSIMalformedInputDoesNotPanic is a regression test: a theme
// file with a malformed color (short hex, non-hex characters, empty
// string) must degrade to the terminal's default foreground instead of
// panicking the editor on startup via a slice-bounds-out-of-range.
func TestHexToANSIMalformedInputDoesNotPanic(t *testing.T) {
	for _, hex := range []string{"#fff", "#12345", "#gggggg", "red", "", "#"} {
		got := hexToANSI(hex)
		if got != defaultFgANSI {
			t.Errorf("hexToANSI(%q) = %q, want default fallback %q", hex, got, defaultFgANSI)
		}
	}
}

func TestHexToANSIValidInput(t *testing.T) {
	got := hexToANSI("#569CD6")
	want := "\x1b[38;2;86;156;214m"
	if got != want {
		t.Errorf("hexToANSI(#569CD6) = %q, want %q", got, want)
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

func TestIsInStringNilSafe(t *testing.T) {
	var h *Highlighter
	if h.IsInString([]byte("anything"), 0, 0) {
		t.Error("nil Highlighter.IsInString() = true, want false")
	}
}

func TestIsInString(t *testing.T) {
	h := New("test.go")
	if h == nil {
		t.Skip("no Go highlighter registered; run with -tags lang_all (or lang_go)")
	}

	content := []byte(`package main

var s = "hello world"
var n = 1
`)
	tests := []struct {
		name       string
		line, col  int
		wantString bool
	}{
		{"inside string body", 2, 12, true},
		{"just inside opening quote", 2, 9, true},
		{"just before closing quote", 2, 20, true},
		{"right before opening quote (boundary, outside)", 2, 8, false},
		{"right after closing quote (boundary, outside)", 2, 21, false},
		{"well before string", 2, 5, false},
		{"unrelated line", 3, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := h.IsInString(content, tt.line, tt.col); got != tt.wantString {
				t.Errorf("IsInString(line=%d, col=%d) = %v, want %v", tt.line, tt.col, got, tt.wantString)
			}
		})
	}
}
