package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useTempDir points the package at a per-test directory and resets the prune
// throttle, so tests neither touch the real temp dir nor inherit each other's
// last-prune time.
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", dir)
	mu.Lock()
	lastPrune = time.Time{}
	mu.Unlock()
	return dir
}

func TestWriteGoesToDatedFile(t *testing.T) {
	dir := useTempDir(t)
	// One date for both the write and the assertion. Formatting time.Now()
	// again after Write would disagree with the file Write actually opened if
	// the two calls straddled midnight — rare, but a test that fails once a
	// year at 00:00 is worse than one that never does.
	day := time.Now().Format("2006-01-02")
	Write("app", "hello %d", 7)

	want := filepath.Join(dir, "indigo-plugins-"+day+".log")
	content, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("reading %s: %v", want, err)
	}
	if got := string(content); got != "[app] hello 7\n" {
		t.Errorf("log content = %q, want %q", got, "[app] hello 7\n")
	}
}

func TestWriteWithoutTagIsUnprefixed(t *testing.T) {
	useTempDir(t)
	Write("", "plain line")

	content, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("reading %s: %v", Path(), err)
	}
	if got := string(content); got != "plain line\n" {
		t.Errorf("log content = %q, want %q", got, "plain line\n")
	}
}

// TestPruneDeletesOnlyStaleFiles covers the retention rule that replaced the
// old single unbounded file: anything not written to for 24h goes, everything
// newer stays — including the legacy un-rotated name, which the glob matches.
func TestPruneDeletesOnlyStaleFiles(t *testing.T) {
	dir := useTempDir(t)

	stale := filepath.Join(dir, "indigo-plugins-2020-01-01.log")
	legacy := filepath.Join(dir, "indigo-plugins.log")
	fresh := filepath.Join(dir, "indigo-plugins-2020-01-02.log")
	unrelated := filepath.Join(dir, "something-else.log")
	for _, p := range []string{stale, legacy, fresh, unrelated} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{stale, legacy, unrelated} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	Prune()

	for _, p := range []string{stale, legacy} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after Prune, want deleted", filepath.Base(p))
		}
	}
	// fresh was written just now; unrelated is old but isn't one of ours.
	for _, p := range []string{fresh, unrelated} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was deleted by Prune, want kept: %v", filepath.Base(p), err)
		}
	}
}

// TestPruneKeepsCurrentFileEvenIfStale is the guard against a wrong clock (or
// a machine resumed from sleep) deleting the file being written right now:
// today's path is skipped before its mtime is ever consulted.
func TestPruneKeepsCurrentFileEvenIfStale(t *testing.T) {
	useTempDir(t)
	Write("app", "current")

	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(Path(), old, old); err != nil {
		t.Fatal(err)
	}

	Prune()

	if _, err := os.Stat(Path()); err != nil {
		t.Errorf("current log file was pruned: %v", err)
	}
}

// TestWritePrunesOnceThenThrottles verifies logging itself drives the cleanup
// (no process schedules it), while a second write skips the directory scan —
// pruning per line would cost a glob and a stat on every RPC.
func TestWritePrunesOnceThenThrottles(t *testing.T) {
	dir := useTempDir(t)
	stale := filepath.Join(dir, "indigo-plugins-2020-01-01.log")
	if err := os.WriteFile(stale, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	Write("app", "first")
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale file survived the first Write, which should have pruned")
	}

	// A second stale file appearing right after must NOT be picked up until
	// the throttle expires.
	stale2 := filepath.Join(dir, "indigo-plugins-2020-01-02.log")
	if err := os.WriteFile(stale2, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale2, old, old); err != nil {
		t.Fatal(err)
	}
	Write("app", "second")
	if _, err := os.Stat(stale2); err != nil {
		t.Errorf("second Write pruned again despite the %v throttle: %v", pruneInterval, err)
	}
}

func TestPathRollsOverByDate(t *testing.T) {
	useTempDir(t)
	// Bracket the call rather than comparing against a single later
	// time.Now(): either date is correct if the clock crossed midnight
	// mid-test, and accepting both is what makes this deterministic.
	before := time.Now()
	got := filepath.Base(Path())
	after := time.Now()

	if !strings.HasPrefix(got, "indigo-plugins-") || !strings.HasSuffix(got, ".log") {
		t.Fatalf("Path() = %q, want indigo-plugins-<date>.log", got)
	}
	name := func(at time.Time) string { return "indigo-plugins-" + at.Format("2006-01-02") + ".log" }
	if got != name(before) && got != name(after) {
		t.Errorf("Path() = %q, want %q", got, name(after))
	}
}

// TestOpenRefusesSymlinkedLogPath covers the shared-temp-directory hazard: the
// log's name is a predictable function of the date, so on a multi-user machine
// with /tmp as os.TempDir() another user can pre-create it as a symlink and
// have every log line appended to a file of their choosing. The open must fail
// instead of following it.
func TestOpenRefusesSymlinkedLogPath(t *testing.T) {
	dir := useTempDir(t)
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path()); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if f, err := Open(); err == nil {
		f.Close() //nolint:errcheck
		t.Error("Open() followed a symlink at the log path, want an error")
	}

	Write("app", "should not be written")

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "original\n" {
		t.Errorf("symlink target was appended to: %q", content)
	}
}

// TestOpenCreatesPrivateFile: log contents are buffer text and file paths, so
// on a shared /tmp they must not be world-readable.
func TestOpenCreatesPrivateFile(t *testing.T) {
	useTempDir(t)
	Write("app", "private")

	fi, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0077 != 0 {
		t.Errorf("log file mode = %o, want no group/other access", perm)
	}
}
