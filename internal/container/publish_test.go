package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPublish runs publishScript with a local /bin/sh — the same POSIX shell
// contract the container provides — so the rename logic is exercised for real
// rather than only asserted as a string.
func runPublish(t *testing.T, tmp, dst string) error {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", publishScript)
	cmd.Env = append(os.Environ(), "INDIGO_TMP="+tmp, "INDIGO_DST="+dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("publish output: %s", out)
	}
	return err
}

// The regression: CopyIn used to write straight to the content-addressed
// destination, so an interrupted copy left a truncated, executable file there
// that every later attach trusted. The destination must only ever appear by
// rename of a completed copy.
func TestPartialPathIsBesideDestinationAndUnique(t *testing.T) {
	dst := "/tmp/.indigo-server-arm64-abc"
	a, b := partialPath(dst), partialPath(dst)
	if filepath.Dir(a) != filepath.Dir(dst) || !strings.HasPrefix(a, dst+".partial-") {
		t.Errorf("partialPath(%q) = %q, want a sibling of the destination", dst, a)
	}
	if a == b {
		t.Errorf("two partial paths collided: %q", a)
	}
}

func TestPublishMovesFileIntoPlace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "server")
	tmp := partialPath(dst)
	if err := os.WriteFile(tmp, []byte("complete"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPublish(t, tmp, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "complete" {
		t.Fatalf("dst = %q, %v; want the published copy", got, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("temporary copy still present after publish: %v", err)
	}
}

// Two windows copying the same plugin set: the loser must discard its copy
// rather than move it inside the winner's directory, which is what a bare
// `mv src existing-dir` does.
func TestPublishDirectoryWhenAnotherWindowWon(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(filepath.Join(dst, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := partialPath(dst)
	if err := os.MkdirAll(filepath.Join(tmp, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPublish(t, tmp, dst); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "git" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dst entries = %v, want [git] (loser's copy nested inside?)", names)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("loser's temporary copy was not discarded: %v", err)
	}
}

func TestPublishDirectoryIntoPlace(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "plugins")
	tmp := partialPath(dst)
	if err := os.MkdirAll(filepath.Join(tmp, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPublish(t, tmp, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "git")); err != nil {
		t.Fatalf("published directory missing its contents: %v", err)
	}
}
