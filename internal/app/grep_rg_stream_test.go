package app

import (
	"bufio"
	"fmt"
	"io"
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

// withMaxFileBytes temporarily overrides maxFileBytes for the duration of a
// test.
func withMaxFileBytes(t *testing.T, n int) {
	t.Helper()
	orig := maxFileBytes
	maxFileBytes = n
	t.Cleanup(func() { maxFileBytes = orig })
}

// TestSearchWithRgSkipsOversizedRecordWithoutLosingOtherMatches is a
// regression test for the review finding on the fix above: the original
// version of this test (and the code it exercised) treated an
// over-buffer-size line as a fatal error — killing rg and discarding every
// match found so far, even ones already read before the oversized line.
// The correct behavior, matching searchBuiltin's existing "skip files over
// maxFileBytes" policy, is to skip just the oversized record and keep
// going: matches before *and after* it must both still come back, with no
// error.
func TestSearchWithRgSkipsOversizedRecordWithoutLosingOtherMatches(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	withMaxFileBytes(t, 200) // small cap so the test fixture doesn't need a multi-MB oversized line

	dir := t.TempDir()
	oversizedLine := strings.Repeat("x", 500) // well past the 200-byte cap above
	script := "#!/bin/sh\n" +
		`echo '{"type":"match","data":{"path":{"text":"/tmp/fake.go"},"lines":{"text":"first match"},"line_number":1,"submatches":[{"start":0,"end":5}]}}'` + "\n" +
		"echo '" + oversizedLine + "'\n" +
		`echo '{"type":"match","data":{"path":{"text":"/tmp/fake.go"},"lines":{"text":"second match"},"line_number":2,"submatches":[{"start":0,"end":6}]}}'` + "\n"
	scriptPath := writeFakeRg(t, dir, "fake-rg-oversized-record.sh", script)
	useFakeRg(t, scriptPath)

	results, err := searchWithRg(dir, "hello", "", "", false, false)
	if err != nil {
		t.Fatalf("searchWithRg: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (matches before and after the oversized record)", len(results))
	}
	if results[0].LineText != "first match" || results[1].LineText != "second match" {
		t.Errorf("results = %+v, want first/second match preserved around the skipped oversized record", results)
	}
}

// TestReadCappedLineBoundsMemoryOnOversizedRecord verifies readCappedLine
// itself: a record far longer than maxLen is reported as oversized without
// requiring the caller to have buffered the full oversized record (unlike
// bufio.Reader.ReadBytes, which has no such cap at all), and reading
// resumes correctly at the next record afterward.
func TestReadCappedLineBoundsMemoryOnOversizedRecord(t *testing.T) {
	huge := strings.Repeat("z", 50000) // much larger than the 100-byte cap below
	input := "short\n" + huge + "\nafter\n"
	reader := bufio.NewReader(strings.NewReader(input))

	// readCappedLine includes the trailing delimiter in a normal record,
	// same as bufio.Reader.ReadBytes/ReadSlice — callers (searchWithRg)
	// trim it themselves, same as they already do for bufio.Scanner output.
	line, oversized, err := readCappedLine(reader, 100)
	if err != nil || oversized || string(line) != "short\n" {
		t.Fatalf("1st record: got (%q, %v, %v), want (\"short\\n\", false, nil)", line, oversized, err)
	}

	line, oversized, err = readCappedLine(reader, 100)
	if err != nil || !oversized || line != nil {
		t.Fatalf("2nd record: got (%q, %v, %v), want (nil, true, nil)", line, oversized, err)
	}

	line, oversized, err = readCappedLine(reader, 100)
	if err != nil || oversized || string(line) != "after\n" {
		t.Fatalf("3rd record: got (%q, %v, %v), want (\"after\\n\", false, nil)", line, oversized, err)
	}

	_, _, err = readCappedLine(reader, 100)
	if err != io.EOF {
		t.Fatalf("4th record: got err %v, want io.EOF", err)
	}
}
