package app

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// withFileListCacheTTL temporarily overrides fileListCacheTTL for the
// duration of a test.
func withFileListCacheTTL(t *testing.T, ttl time.Duration) {
	t.Helper()
	orig := fileListCacheTTL
	fileListCacheTTL = ttl
	t.Cleanup(func() { fileListCacheTTL = orig })
}

// TestSearchBuiltinCachesCandidateFileList is a regression test for the
// caching gap the user reported: running the same (or a different) search
// against the same workspace a second time should not re-walk the
// filesystem, as long as the cache hasn't expired. walkCandidateFiles bumps
// candidateWalkCount every time it actually runs, so a delta of exactly 1
// across two searches proves the second one was served from cache.
func TestSearchBuiltinCachesCandidateFileList(t *testing.T) {
	withFileListCacheTTL(t, time.Minute)

	dir := writeTree(t, map[string]string{
		"a.go": "hello world\n",
		"b.go": "nothing here\n",
	})

	before := atomic.LoadInt64(&candidateWalkCount)

	if _, err := searchBuiltin(dir, "hello", "", "", true, false); err != nil {
		t.Fatalf("first search: %v", err)
	}
	afterFirst := atomic.LoadInt64(&candidateWalkCount)
	if afterFirst != before+1 {
		t.Fatalf("first search: walk count went from %d to %d, want exactly +1 (cache miss)", before, afterFirst)
	}

	// A second search — even with a different pattern and glob filter —
	// should reuse the cached file list rather than walking again.
	if _, err := searchBuiltin(dir, "nothing", "*.go", "", true, false); err != nil {
		t.Fatalf("second search: %v", err)
	}
	afterSecond := atomic.LoadInt64(&candidateWalkCount)
	if afterSecond != afterFirst {
		t.Fatalf("second search: walk count went from %d to %d, want unchanged (cache hit)", afterFirst, afterSecond)
	}
}

// TestSearchBuiltinCacheExpiresAndPicksUpNewFiles verifies the cache is
// bounded-stale rather than permanently stale: a file created after the
// first search isn't found while the cache is still fresh, but is found
// once the (shrunk-for-this-test) TTL has elapsed and the list is
// re-walked.
func TestSearchBuiltinCacheExpiresAndPicksUpNewFiles(t *testing.T) {
	withFileListCacheTTL(t, 20*time.Millisecond)

	dir := writeTree(t, map[string]string{
		"a.go": "target\n",
	})

	results, err := searchBuiltin(dir, "target", "", "", true, false)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("first search: got %d results, want 1", len(results))
	}

	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("target\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Cache still fresh: the new file shouldn't be picked up yet.
	results, err = searchBuiltin(dir, "target", "", "", true, false)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("second search (cache still fresh): got %d results, want 1 (new file not yet visible)", len(results))
	}

	time.Sleep(60 * time.Millisecond)

	results, err = searchBuiltin(dir, "target", "", "", true, false)
	if err != nil {
		t.Fatalf("third search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("third search (cache expired): got %d results, want 2 (new file now visible)", len(results))
	}
}
