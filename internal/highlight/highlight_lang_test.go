//go:build lang_go || lang_all

package highlight

import "testing"

var goSample = []byte(`package main

import "fmt"

// greet prints a greeting.
func greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

func main() {
	fmt.Println(greet("world"))
}
`)

func TestGoHighlighterCreated(t *testing.T) {
	for _, path := range []string{"main.go", "pkg/foo/bar.go", "/abs/path/x.go"} {
		if h := New(path); h == nil {
			t.Errorf("New(%q) = nil, want Highlighter", path)
		}
	}
}

func TestGoHighlightReturnsSpans(t *testing.T) {
	h := New("main.go")
	if h == nil {
		t.Fatal("New(main.go) = nil")
	}
	spans := h.Highlight(goSample)
	if len(spans) == 0 {
		t.Fatal("Highlight returned no spans for Go source")
	}
	// Line 0 ("package main") must have keyword and module spans.
	line0 := spans[0]
	if len(line0) == 0 {
		t.Error("expected spans on line 0 (package main)")
	}
}

func TestGoHighlightCommentSpan(t *testing.T) {
	h := New("main.go")
	if h == nil {
		t.Fatal("New(main.go) = nil")
	}
	// Line 4 is "// greet prints a greeting." — should be a comment span.
	spans := h.Highlight(goSample)
	commentLine := spans[4]
	if len(commentLine) == 0 {
		t.Error("expected comment span on line 4")
		return
	}
	// The first (highest-priority) span on the comment line should start at 0.
	if commentLine[0].StartCol != 0 {
		t.Errorf("comment span StartCol = %d, want 0", commentLine[0].StartCol)
	}
}

func TestGoHighlightConcurrent(t *testing.T) {
	h := New("main.go")
	if h == nil {
		t.Fatal("New(main.go) = nil")
	}
	// Highlight is documented as safe to call concurrently.
	done := make(chan struct{}, 4)
	for range 4 {
		go func() {
			h.Highlight(goSample)
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
}
