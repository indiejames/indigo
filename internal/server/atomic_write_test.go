package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.go")

	// New file gets the default mode.
	if err := atomicWriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("atomicWriteFile new: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v1" {
		t.Errorf("content = %q, want v1", got)
	}

	// Existing file's permissions are preserved on rewrite.
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(path, []byte("v2"), 0644); err != nil {
		t.Fatalf("atomicWriteFile rewrite: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "v2" {
		t.Errorf("content = %q, want v2", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0755 {
		t.Errorf("mode = %o, want 0755 preserved", fi.Mode().Perm())
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("directory has leftovers: %v", names)
	}
}
