package main

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"
)

// TestBookmarksPersistWritesLandInMutationOrder is a regression test for a
// bug distinct from (and one layer deeper than) the data race
// TestBookmarksNoRaceBetweenEditEventAndPersist covers: a snapshot copy
// alone only protects the in-memory read from a concurrent mutation — it
// does nothing to order the actual disk writes. Before enqueuePersist,
// onAltM/onEditEvent each called persistBookmarks directly (synchronously or
// via `go`) from their own goroutine; those file writes had no ordering
// guarantee relative to each other even though their snapshots were taken
// in the correct mutation order under b.mu, so an older snapshot's write
// landing on disk *after* a newer one's would silently revert
// bookmarks.json to stale state.
//
// This drives many concurrent mutate-and-enqueue calls (the same shape
// onAltM/onEditEvent use: lock, mutate, enqueuePersist, unlock) with
// monotonically distinguishable payloads, then waits for the queue to fully
// drain and asserts the file matches the *last* mutation — the single
// persistWorker's FIFO processing makes this a deterministic guarantee, not
// a probabilistic one, which is exactly what distinguishes it from the old
// every-goroutine-writes-independently approach.
func TestBookmarksPersistWritesLandInMutationOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	b := &Bookmarks{persistCh: make(chan []bookmark, 64)}
	workerDone := make(chan struct{})
	go func() {
		b.persistWorker()
		close(workerDone)
	}()
	// See bookmarks_test.go's TestBookmarksNoRaceBetweenEditEventAndPersist
	// for why draining before return matters with t.Setenv("HOME", ...).
	defer func() {
		close(b.persistCh)
		<-workerDone
	}()

	const n = 300
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b.mu.Lock()
			b.bookmarks = []bookmark{{filePath: "/tmp/a.go", line: uint32(i), active: true}}
			b.enqueuePersist(b.bookmarks)
			b.mu.Unlock()
		}(i)
	}
	wg.Wait()

	// Enqueue a distinguishable sentinel *after* every other send has
	// returned (wg.Wait() above guarantees each send() call — not
	// necessarily each worker write — has completed) and wait for it to
	// reach disk. Since persistCh is FIFO and drained by one worker, seeing
	// the sentinel on disk guarantees every prior write already landed.
	const sentinel = 999999
	b.mu.Lock()
	b.bookmarks = []bookmark{{filePath: "/tmp/a.go", line: sentinel, active: true}}
	b.enqueuePersist(b.bookmarks)
	b.mu.Unlock()

	path, err := bookmarksFilePath()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var lastLine uint32 = 1<<32 - 1
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			var saved []savedBookmark
			if json.Unmarshal(data, &saved) == nil && len(saved) == 1 {
				lastLine = saved[0].Line
				if lastLine == sentinel {
					return // success: the file settled on the last write, as required
				}
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("bookmarks.json never converged to the sentinel write (last observed line: %d)", lastLine)
}
