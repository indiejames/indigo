package server

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoadContentReportsUnreadableFile is a regression test: loadContent used
// to discard os.ReadFile's error entirely, so a file that exists but can't be
// read (permissions, an I/O error on a network mount, EMFILE under load) was
// indistinguishable from one that doesn't exist yet — both produced empty
// content with no error. OpenFile would then hand the client an empty buffer
// for a file that actually has content, misrepresenting it and risking
// overwriting it wholesale on the next save.
func TestLoadContentReportsUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits don't prevent reads")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadability is POSIX-specific")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unreadable.txt")
	if err := os.WriteFile(path, []byte("IMPORTANT CONTENT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) }) //nolint:errcheck

	s := &editorService{buffers: map[uint32]*bufferEntry{}, recDir: t.TempDir()}
	content, fromRecovery, err := s.loadContent(path)

	if err == nil {
		t.Fatalf("loadContent returned no error for an unreadable file (content=%q, fromRecovery=%v) — "+
			"the caller can't tell this from a legitimately-empty new file", content, fromRecovery)
	}
	if content != "" {
		t.Errorf("content = %q, want empty alongside the error", content)
	}
}

// TestLoadContentEmptyCasesAreNotErrors pins the cases that must keep
// returning empty content with no error, so the fix above can't regress into
// failing an ordinary new-file open.
func TestLoadContentEmptyCasesAreNotErrors(t *testing.T) {
	s := &editorService{buffers: map[uint32]*bufferEntry{}, recDir: t.TempDir()}

	t.Run("untitled buffer", func(t *testing.T) {
		content, fromRecovery, err := s.loadContent("")
		if err != nil || content != "" || fromRecovery {
			t.Errorf("loadContent(\"\") = (%q, %v, %v), want (\"\", false, nil)", content, fromRecovery, err)
		}
	})

	t.Run("file does not exist yet", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "brand-new.go")
		content, fromRecovery, err := s.loadContent(path)
		if err != nil || content != "" || fromRecovery {
			t.Errorf("loadContent(new file) = (%q, %v, %v), want (\"\", false, nil)", content, fromRecovery, err)
		}
	})

	t.Run("existing readable file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "a.go")
		if err := os.WriteFile(path, []byte("package a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		content, fromRecovery, err := s.loadContent(path)
		if err != nil || content != "package a\n" || fromRecovery {
			t.Errorf("loadContent(readable) = (%q, %v, %v), want (\"package a\\n\", false, nil)", content, fromRecovery, err)
		}
	})
}
