package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPublish runs a publish script with a local /bin/sh — the same POSIX shell
// contract the container provides — so the logic is exercised for real rather
// than only asserted as a string.
func runPublish(t *testing.T, script, tmp, dst string) error {
	t.Helper()
	cmd := exec.Command(shellForTest, "-c", script)
	cmd.Env = append(os.Environ(), "INDIGO_TMP="+tmp, "INDIGO_DST="+dst)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("publish output: %s", out)
	}
	return err
}

// withoutFastPath strips a script's leading existence check, leaving only the
// atomic primitive — the state of two publishers that both passed the check
// before either published, which is the race the primitive exists for.
func withoutFastPath(t *testing.T, script, fastPath string) string {
	t.Helper()
	if !strings.Contains(script, fastPath) {
		t.Fatalf("fast path %q not found in script; test is stale", fastPath)
	}
	return strings.Replace(script, fastPath, "", 1)
}

const (
	fileFastPath = `[ ! -e "$INDIGO_DST" ] && `
	dirFastPath  = `if [ -e "$INDIGO_DST" ]; then rm -rf -- "$INDIGO_TMP"; exit 0; fi; `
)

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

// The regression: CopyIn used to write straight to the content-addressed
// destination, so an interrupted copy left a truncated, executable file there
// that every later attach trusted. The destination must only ever appear
// complete.
func TestPublishFileIntoPlace(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "server")
	tmp := partialPath(dst)
	if err := os.WriteFile(tmp, []byte("complete"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPublish(t, publishFileScript, tmp, dst); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "complete" {
		t.Fatalf("dst = %q, %v; want the published copy", got, err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("staging copy still present after publish: %v", err)
	}
}

// A publisher that loses the race must leave the winner's file in place and
// discard its own — with and without the fast path.
func TestPublishFileLoserPreservesWinner(t *testing.T) {
	for name, script := range map[string]string{
		"fast path": publishFileScript,
		"race":      withoutFastPath(t, publishFileScript, fileFastPath),
	} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "server")
			if err := os.WriteFile(dst, []byte("winner"), 0o755); err != nil {
				t.Fatal(err)
			}
			tmp := partialPath(dst)
			if err := os.WriteFile(tmp, []byte("loser"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := runPublish(t, script, tmp, dst); err != nil {
				t.Fatalf("losing publisher failed: %v (want success: the file it wanted is in place)", err)
			}
			if got, _ := os.ReadFile(dst); string(got) != "winner" {
				t.Errorf("dst = %q, want the winner's copy preserved", got)
			}
			if _, err := os.Stat(tmp); !os.IsNotExist(err) {
				t.Errorf("loser's staging copy was not discarded: %v", err)
			}
		})
	}
}

func TestPublishDirectoryIntoPlace(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "plugins")
	tmp := partialPath(dst)
	if err := os.MkdirAll(filepath.Join(tmp, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runPublish(t, publishDirScript, tmp, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "git")); err != nil {
		t.Fatalf("published directory missing its contents: %v", err)
	}
}

// Two windows copying the same plugin set: the loser must discard its copy
// rather than nest it inside the winner's, which is what `mv src existing-dir`
// does — and, without -n, what `ln -s` onto an existing symlink to a directory
// does too.
func TestPublishDirectoryLoserDoesNotNest(t *testing.T) {
	for name, script := range map[string]string{
		"fast path": publishDirScript,
		"race":      withoutFastPath(t, publishDirScript, dirFastPath),
	} {
		t.Run(name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "plugins")
			winner := partialPath(dst)
			if err := os.MkdirAll(filepath.Join(winner, "git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := runPublish(t, publishDirScript, winner, dst); err != nil {
				t.Fatal(err)
			}
			loser := partialPath(dst)
			if err := os.MkdirAll(filepath.Join(loser, "git"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := runPublish(t, script, loser, dst); err != nil {
				t.Fatalf("losing publisher failed: %v (want success)", err)
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
				t.Errorf("dst entries = %v, want [git] (loser nested inside the winner?)", names)
			}
			if _, err := os.Stat(loser); !os.IsNotExist(err) {
				t.Errorf("loser's staging copy was not discarded: %v", err)
			}
			if _, err := os.Stat(winner); err != nil {
				t.Errorf("winner's storage removed: %v", err)
			}
		})
	}
}

// shellForTest is /bin/sh; INDIGO_TEST_SH overrides it to run these against
// another POSIX shell (dash, busybox ash), closer to what a container has.
var shellForTest = func() string {
	if s := os.Getenv("INDIGO_TEST_SH"); s != "" {
		return s
	}
	return "/bin/sh"
}()
