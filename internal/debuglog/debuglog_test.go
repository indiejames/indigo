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
	modeCheckedPath = ""
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
	// The line is timestamped, so match the tag and message rather than the
	// whole line; TestWriteTimestampsLines covers the timestamp itself.
	if got := string(content); !strings.HasSuffix(got, " [app] hello 7\n") {
		t.Errorf("log content = %q, want it to end with %q", got, " [app] hello 7\n")
	}
}

func TestWriteWithoutTagIsUnprefixed(t *testing.T) {
	useTempDir(t)
	Write("", "plain line")

	content, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("reading %s: %v", Path(), err)
	}
	if got := string(content); !strings.HasSuffix(got, " plain line\n") || strings.Contains(got, "[") {
		t.Errorf("log content = %q, want a timestamp then %q with no bracketed tag", got, "plain line")
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

// The log holds buffer text, file paths and plugin output, so it must be
// readable only by the user running indigo. The mode passed to os.OpenFile
// only applies when that call creates the file, so an already-existing one —
// left by an older indigo, or created first by another user on a shared /tmp,
// where the dated filename is entirely predictable — would otherwise be
// appended to at whatever mode it already had.
func TestExistingLogFileIsTightenedBeforeWriting(t *testing.T) {
	dir := useTempDir(t)
	path := filepath.Join(dir, "indigo-plugins-"+time.Now().Format("2006-01-02")+".log")

	if err := os.WriteFile(path, []byte("planted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Explicitly, because the mode above is masked by the umask — under a
	// restrictive one the file would already be 0600 and the test would pass
	// while proving nothing.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	Write("test", "after")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != logFileMode {
		t.Errorf("mode = %o, want %o — a pre-existing log was appended to at its own mode", got, logFileMode)
	}
	// Tightening must not cost the file its contents: this is an append-only
	// log, and a reader is mid-file.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "planted") || !strings.Contains(string(b), "after") {
		t.Errorf("content = %q, want the existing line kept and the new one appended", b)
	}
}

// A file this process creates itself gets the mode from the open, which the
// check above must not be allowed to mask a regression in.
func TestNewLogFileIsOwnerOnly(t *testing.T) {
	dir := useTempDir(t)
	Write("test", "first line")
	info, err := os.Stat(filepath.Join(dir, "indigo-plugins-"+time.Now().Format("2006-01-02")+".log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != logFileMode {
		t.Errorf("mode = %o, want %o", got, logFileMode)
	}
}

// Open reports the failure rather than handing back a file at a mode it could
// not fix, so the caller writes nothing into it. The realistic trigger —
// fchmod returning EPERM on a file another user owns — needs a second uid and
// so cannot be driven from a test; this drives the same branch through
// ensureMode with a closed descriptor, which is the one other way fchmod
// fails.
func TestOpenRefusesAFileItCannotTighten(t *testing.T) {
	dir := useTempDir(t)
	path := filepath.Join(dir, "probe.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close() //nolint:errcheck
	if err := ensureMode(path, f); err == nil {
		t.Error("ensureMode returned nil for a descriptor it could not chmod")
	}
	mu.Lock()
	cached := modeCheckedPath
	mu.Unlock()
	if cached == path {
		t.Error("a failed check was cached, so later opens would skip it and write anyway")
	}
}
