package agenttools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/indiejames/indigo/internal/client"
)

// TestReportBundleCreatesADistinctFile covers the bundle being written with
// os.CreateTemp rather than os.WriteFile to a name built from the clock.
//
// Two things were wrong with the old name. It collided: two bundles in the same
// second wrote to one path, and the second silently truncated the first. And it
// was entirely predictable in a directory that defaults to os.TempDir() — a
// shared /tmp on a multi-user box — with O_CREATE but no O_EXCL, so another
// user could pre-create or symlink that exact path and have this process write
// through it. debuglog opens its own files O_NOFOLLOW for precisely this
// reason; this was the one write in the same directory that did not.
func TestReportBundleCreatesADistinctFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INDIGO_LOG_DIR", dir)

	// A zero RPC: GetSyncState fails and the bundle records it as unavailable,
	// which is enough — this is about the file, not the contents.
	rpc := &client.RPC{}

	first, isErr := execReportBundle(context.Background(), rpc, dir, reportBundleInput{})
	if isErr {
		t.Fatalf("execReportBundle: %s", first)
	}
	second, isErr := execReportBundle(context.Background(), rpc, dir, reportBundleInput{})
	if isErr {
		t.Fatalf("execReportBundle: %s", second)
	}

	pathOf := func(summary string) string {
		// The summary's first line is "wrote <path> (N bytes)".
		fields := strings.Fields(summary)
		if len(fields) < 2 {
			t.Fatalf("cannot find the path in summary %q", summary)
		}
		return fields[1]
	}
	p1, p2 := pathOf(first), pathOf(second)
	if p1 == p2 {
		t.Errorf("both bundles wrote to %s — the second silently truncated the first", p1)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var reports int
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "indigo-report-") {
			continue
		}
		reports++
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600: a bundle in a shared /tmp must not "+
				"be world-readable", e.Name(), perm)
		}
		if fi.Size() == 0 {
			t.Errorf("%s is empty", e.Name())
		}
	}
	if reports != 2 {
		t.Errorf("found %d report files, want 2", reports)
	}
}
