package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
)

func TestExtractRangeSingleLine(t *testing.T) {
	buf := document.New("a.go", "foo bar baz\n")
	got, err := extractRange(buf, 0, 4, 0, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bar" {
		t.Errorf("extractRange = %q, want %q", got, "bar")
	}
}

func TestExtractRangeMultiLine(t *testing.T) {
	buf := document.New("a.go", "func f() {\n\treturn\n}\nfunc g() {}\n")
	got, err := extractRange(buf, 0, 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := "func f() {\n\treturn\n}"
	if got != want {
		t.Errorf("extractRange = %q, want %q", got, want)
	}
}

func TestExtractRangeInvertedRange(t *testing.T) {
	buf := document.New("a.go", "foo\n")
	if _, err := extractRange(buf, 0, 3, 0, 1); err == nil {
		t.Error("expected an error for an inverted range")
	}
}

func TestExtractRangeOutOfBoundsLine(t *testing.T) {
	buf := document.New("a.go", "foo\n")
	if _, err := extractRange(buf, 0, 0, 5, 0); err == nil {
		t.Error("expected an error for an out-of-range line")
	}
}

func TestExtractRangeOutOfBoundsCol(t *testing.T) {
	buf := document.New("a.go", "foo\n")
	if _, err := extractRange(buf, 0, 0, 0, 99); err == nil {
		t.Error("expected an error for an out-of-range column")
	}
}

func TestAppendOpsForBufferWithExistingContent(t *testing.T) {
	buf := document.New("a.go", "package a\n\nfunc f() {}\n")
	for _, op := range appendOpsForBuffer(buf, 1, "func g() {}") {
		buf.Apply(op)
	}

	want := "package a\n\nfunc f() {}\n\nfunc g() {}"
	if got := buf.Content(); got != want {
		t.Errorf("buf.Content() = %q, want %q", got, want)
	}
}

func TestAppendOpsForBufferWithMultipleTrailingNewlines(t *testing.T) {
	// Trailing blank lines in the source shouldn't produce extra blank
	// lines around the appended text.
	buf := document.New("a.go", "func f() {}\n\n\n")
	for _, op := range appendOpsForBuffer(buf, 1, "func g() {}") {
		buf.Apply(op)
	}

	want := "func f() {}\n\nfunc g() {}"
	if got := buf.Content(); got != want {
		t.Errorf("buf.Content() = %q, want %q", got, want)
	}
}

func TestAppendOpsForBufferEmpty(t *testing.T) {
	buf := document.New("a.go", "")
	for _, op := range appendOpsForBuffer(buf, 1, "func g() {}") {
		buf.Apply(op)
	}

	if got := buf.Content(); got != "func g() {}" {
		t.Errorf("buf.Content() = %q, want %q", got, "func g() {}")
	}
}

func TestAppendedContent(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		text     string
		want     string
	}{
		{"empty file", "", "func g() {}", "func g() {}\n"},
		{"trailing newline", "package a\n", "func g() {}", "package a\n\nfunc g() {}\n"},
		{"multiple trailing newlines", "package a\n\n\n", "func g() {}", "package a\n\nfunc g() {}\n"},
		{"no trailing newline", "package a", "func g() {}", "package a\n\nfunc g() {}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := appendedContent(c.existing, c.text); got != c.want {
				t.Errorf("appendedContent(%q, %q) = %q, want %q", c.existing, c.text, got, c.want)
			}
		})
	}
}

func TestAppendTextToFileOnDiskExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &editorService{savingPaths: make(map[string]time.Time)}
	if err := s.appendTextToFile(1, path, "func g() {}"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "package a\n\nfunc g() {}\n"
	if string(got) != want {
		t.Errorf("file content = %q, want %q", string(got), want)
	}
}

func TestAppendTextToFileOnDiskNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.go")

	s := &editorService{savingPaths: make(map[string]time.Time)}
	if err := s.appendTextToFile(1, path, "func g() {}"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "func g() {}\n"
	if string(got) != want {
		t.Errorf("file content = %q, want %q", string(got), want)
	}
}
