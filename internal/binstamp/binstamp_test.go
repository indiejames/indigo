package binstamp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOfReportsSizeAndModTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	write(t, path, "hello")

	got, ok := Of(path)
	if !ok {
		t.Fatal("Of() ok = false for an existing file")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Size != fi.Size() || got.ModTime != fi.ModTime().UnixNano() {
		t.Errorf("Of() = %+v, want size %d modtime %d", got, fi.Size(), fi.ModTime().UnixNano())
	}
}

func TestOfMissingFile(t *testing.T) {
	if _, ok := Of(filepath.Join(t.TempDir(), "nope")); ok {
		t.Error("Of() ok = true for a missing file, want false")
	}
}

func TestReplacedFalseWhenUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	write(t, path, "one")
	want, _ := Of(path)

	if Replaced(path, want) {
		t.Error("Replaced() = true for an untouched file")
	}
}

// A rebuild can produce a same-sized binary, so modification time is what has
// to separate the two.
func TestReplacedDetectsSameSizeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	write(t, path, "one")
	want, _ := Of(path)

	write(t, path, "two")
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	if !Replaced(path, want) {
		t.Error("Replaced() = false after a same-size rewrite, want true")
	}
}

func TestReplacedTreatsMissingAsReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin")
	write(t, path, "one")
	want, _ := Of(path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if !Replaced(path, want) {
		t.Error("Replaced() = false for a deleted file, want true")
	}
}
