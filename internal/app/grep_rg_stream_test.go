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
