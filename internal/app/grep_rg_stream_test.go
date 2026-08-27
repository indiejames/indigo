package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeRg writes an executable shell script at dir/name and returns its
// path — a stand-in for a real rg binary, per the rgExecutable seam.
func writeFakeRg(t *testing.T, dir, name, script string) string {
	t.Helper()
	scriptPath := filepath.Join(dir, name)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return scriptPath
}

// useFakeRg points rgExecutable at path for the duration of the test.
func useFakeRg(t *testing.T, path string) {
	t.Helper()
	orig := rgExecutable
	rgExecutable = path
	t.Cleanup(func() { rgExecutable = orig })
}

// TestSearchWithRgKillsProcessEarlyOnceMaxResultsReached is a regression
// test for a bug where searchWithRg used cmd.Output(), which blocks until
// rg exits and buffers all of its stdout before any parsing starts — so the
// maxGrepResults cap only trimmed what was read back afterward and never
// actually stopped rg early. This stands in a fake "rg" that emits far more
// than maxGrepResults matches instantly and then sleeps, so a correct
// implementation (which kills the process once it has enough matches)
// returns almost immediately, while the old cmd.Output()-based
// implementation would block for the full sleep duration.
func TestSearchWithRgKillsProcessEarlyOnceMaxResultsReached(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-rg.sh")

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	extra := maxGrepResults + 200
	for i := 1; i <= extra; i++ {
		fmt.Fprintf(&sb,
			`echo '{"type":"match","data":{"path":{"text":"/tmp/fake.go"},"lines":{"text":"hello world"},"line_number":%d,"submatches":[{"start":0,"end":5}]}}'`+"\n",
			i,
		)
	}
	sb.WriteString("sleep 12\n")
	sb.WriteString("exit 0\n")

	if err := os.WriteFile(scriptPath, []byte(sb.String()), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	orig := rgExecutable
	rgExecutable = scriptPath
	t.Cleanup(func() { rgExecutable = orig })

	start := time.Now()
	results, err := searchWithRg(dir, "hello", "", "", false, false)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("searchWithRg: %v", err)
	}
	if len(results) != maxGrepResults {
		t.Fatalf("got %d results, want %d", len(results), maxGrepResults)
	}
	if elapsed > 6*time.Second {
		t.Fatalf("searchWithRg took %v — process was not killed early (should return well before the fake rg's 12s sleep)", elapsed)
	}
}


// TestSearchWithRgHandlesLongMatchLine is a regression test for
// bufio.Scanner's default 64KB max token size: rg embeds the *entire*
// matching line in each --json message, so a long line (e.g. a minified or
// generated file) bigger than 64KB but well under the buffer searchWithRg
// now configures must still parse correctly rather than silently failing
// to scan.
func TestSearchWithRgHandlesLongMatchLine(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	dir := t.TempDir()
	longText := strings.Repeat("a", 500*1024) // well past the old 64KB default
	msg := fmt.Sprintf(
		`{"type":"match","data":{"path":{"text":"/tmp/fake.go"},"lines":{"text":%q},"line_number":1,"submatches":[{"start":0,"end":1}]}}`,
		longText,
	)
	script := "#!/bin/sh\ncat <<'RGEOF'\n" + msg + "\nRGEOF\n"
	scriptPath := writeFakeRg(t, dir, "fake-rg-longline.sh", script)
	useFakeRg(t, scriptPath)

	results, err := searchWithRg(dir, "hello", "", "", false, false)
	if err != nil {
		t.Fatalf("searchWithRg: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if len(results[0].LineText) != len(longText) {
		t.Errorf("LineText length = %d, want %d", len(results[0].LineText), len(longText))
	}
}

// TestSearchWithRgKillsProcessOnScanError is a regression test: a line
// exceeding even the enlarged scanner buffer makes scanner.Scan() return
// false with a non-nil Err() rather than a clean EOF. The old code treated
// that identically to a clean EOF and fell through to cmd.Wait() — but if
// rg (or, here, the fake standing in for it) is still trying to write more
// output into a pipe nobody is draining anymore, it blocks on that write
// forever, and cmd.Wait() would then hang right alongside it. searchWithRg
// must kill the process instead of waiting on it in that case.
func TestSearchWithRgKillsProcessOnScanError(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("yes not available")
	}

	dir := t.TempDir()
	// One "line" (20MB, no newline) bigger than searchWithRg's scanner
	// buffer, forcing a scan error — followed by a producer ("yes") that
	// never stops on its own, standing in for a still-running rg that would
	// block writing into an undrained pipe if not killed. Built from
	// /dev/zero translated to 'a' rather than "yes | tr -d '\n'": the
	// latter strips one in every two bytes (each "a\n" pair from yes loses
	// its newline), so the resulting line is only half the requested byte
	// count — comfortably under the buffer max instead of over it.
	script := "#!/bin/sh\n" +
		"head -c 20000000 /dev/zero | tr '\\0' 'a'\n" +
		"echo\n" +
		"yes 'still writing more output after the oversized line'\n"
	scriptPath := writeFakeRg(t, dir, "fake-rg-oversized.sh", script)
	useFakeRg(t, scriptPath)

	start := time.Now()
	results, err := searchWithRg(dir, "hello", "", "", false, false)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error from an oversized line, got nil (results: %+v)", results)
	}
	if results != nil {
		t.Errorf("expected nil results on scan error, got %+v", results)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("searchWithRg took %v — rg was not killed promptly after the scan error (possible hang)", elapsed)
	}
}
