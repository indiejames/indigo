package format

import (
	"testing"

	"github.com/indiejames/indigo/internal/config"
)

// ---- expandPath ----

func TestExpandPathTilde(t *testing.T) {
	got := expandPath("~/bin/gofmt")
	if got == "~/bin/gofmt" {
		t.Error("expandPath did not expand ~/")
	}
	if len(got) < 3 {
		t.Errorf("expandPath returned suspiciously short path: %q", got)
	}
}

func TestExpandPathAbsolute(t *testing.T) {
	p := "/usr/local/bin/gofmt"
	if got := expandPath(p); got != p {
		t.Errorf("expandPath(%q) = %q, want unchanged", p, got)
	}
}

func TestExpandPathPlain(t *testing.T) {
	if got := expandPath("gofmt"); got != "gofmt" {
		t.Errorf("expandPath(plain name) = %q, want gofmt", got)
	}
}

// ---- expandArgs ----

func TestExpandArgsFilePlaceholder(t *testing.T) {
	args := []string{"--stdin-filepath", "{file}", "--other"}
	got := expandArgs(args, "/tmp/foo.ts")
	want := []string{"--stdin-filepath", "/tmp/foo.ts", "--other"}
	if len(got) != len(want) {
		t.Fatalf("expandArgs len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("expandArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExpandArgsNoPlaceholder(t *testing.T) {
	args := []string{"-q", "-"}
	got := expandArgs(args, "/tmp/foo.py")
	for i, a := range got {
		if a != args[i] {
			t.Errorf("expandArgs[%d] = %q, want %q (no placeholder)", i, a, args[i])
		}
	}
}

func TestExpandArgsNil(t *testing.T) {
	if got := expandArgs(nil, "/tmp/foo.go"); got != nil {
		t.Errorf("expandArgs(nil) = %v, want nil", got)
	}
}

// ---- matchesExt ----

func TestMatchesExt(t *testing.T) {
	exts := []string{"go", "mod"}
	if !matchesExt(exts, "go") {
		t.Error("matchesExt should match go")
	}
	if !matchesExt(exts, "mod") {
		t.Error("matchesExt should match mod")
	}
	if matchesExt(exts, "rs") {
		t.Error("matchesExt should not match rs")
	}
	if matchesExt(exts, "") {
		t.Error("matchesExt should not match empty string")
	}
}

// ---- runExternal ----

func makeFC(cmd string, args ...string) config.FormatterConfig {
	return config.FormatterConfig{Command: cmd, Args: args}
}

// TestRunExternalPassthrough uses 'cat' to verify stdin→stdout round-trip.
func TestRunExternalPassthrough(t *testing.T) {
	content := "hello\nworld\n"
	got, changed, err := runExternal(makeFC("cat"), "/tmp/test.go", content)
	if err != nil {
		t.Fatalf("runExternal(cat): %v", err)
	}
	if got != content {
		t.Errorf("cat: got %q, want %q", got, content)
	}
	if changed {
		t.Error("cat: changed should be false (content identical)")
	}
}

// TestRunExternalFormatsContent uses tr to verify transformed output is returned.
func TestRunExternalFormatsContent(t *testing.T) {
	got, changed, err := runExternal(makeFC("tr", "a-z", "A-Z"), "/tmp/test.go", "hello\n")
	if err != nil {
		t.Fatalf("runExternal(tr): %v", err)
	}
	if got != "HELLO\n" {
		t.Errorf("tr: got %q, want %q", got, "HELLO\n")
	}
	if !changed {
		t.Error("tr: changed should be true")
	}
}

// TestRunExternalNonZeroExit verifies that a failing formatter returns an error.
func TestRunExternalNonZeroExit(t *testing.T) {
	_, _, err := runExternal(makeFC("false"), "/tmp/test.go", "content")
	if err == nil {
		t.Error("expected error from formatter with non-zero exit, got nil")
	}
}
