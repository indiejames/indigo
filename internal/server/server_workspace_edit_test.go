package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/document"
)

func TestApplyWorkspaceEditsToBuffer(t *testing.T) {
	buf := document.New("test.go", "foo bar foo\nsecond foo line\n")

	items := []workspaceEditItem{
		{origIdx: 0, line: 0, col: 0, oldText: "foo", newText: "quux"},
		{origIdx: 1, line: 0, col: 8, oldText: "foo", newText: "quux"},
		{origIdx: 2, line: 1, col: 7, oldText: "foo", newText: "quux"},
	}

	applied, skipped := applyWorkspaceEditsToBuffer(buf, 1, items)
	if applied != 3 {
		t.Fatalf("applied = %d, want 3", applied)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}

	want := "quux bar quux\nsecond quux line\n"
	if got := buf.Content(); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestApplyWorkspaceEditsToBufferSkipsStaleMatch(t *testing.T) {
	// The second edit's oldText no longer matches (as if another client
	// edited the line concurrently after this edit was queued) — it must be
	// skipped rather than corrupting nearby text.
	buf := document.New("test.go", "foo bar\n")

	items := []workspaceEditItem{
		{origIdx: 0, line: 0, col: 0, oldText: "foo", newText: "quux"},
		{origIdx: 1, line: 0, col: 4, oldText: "baz", newText: "nope"}, // stale: actual text is "bar"
	}

	applied, skipped := applyWorkspaceEditsToBuffer(buf, 1, items)
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	if len(skipped) != 1 || skipped[0] != 1 {
		t.Fatalf("skipped = %v, want [1]", skipped)
	}

	want := "quux bar\n"
	if got := buf.Content(); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestApplyWorkspaceEditsOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("hello world\nhello again\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &editorService{savingPaths: make(map[string]time.Time)}
	items := []workspaceEditItem{
		{origIdx: 0, line: 0, col: 0, oldText: "hello", newText: "goodbye"},
		{origIdx: 1, line: 1, col: 0, oldText: "hello", newText: "goodbye"},
	}

	applied, skipped, err := s.applyWorkspaceEditsOnDisk(path, 1, items)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("applied = %d, want 2", applied)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v, want none", skipped)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "goodbye world\ngoodbye again\n"
	if got := string(data); got != want {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

func TestApplyWorkspaceEditsOnDiskAllSkippedLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	original := "hello world\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	s := &editorService{savingPaths: make(map[string]time.Time)}
	items := []workspaceEditItem{
		{origIdx: 0, line: 0, col: 0, oldText: "stale", newText: "new"},
	}

	applied, skipped, err := s.applyWorkspaceEditsOnDisk(path, 1, items)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("applied = %d, want 0", applied)
	}
	if len(skipped) != 1 {
		t.Fatalf("skipped = %v, want 1 item", skipped)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file content changed to %q, want untouched %q", data, original)
	}
}
