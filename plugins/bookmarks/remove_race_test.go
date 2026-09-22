package main

import (
	"sync"
	"testing"
)

// TestConcurrentRemovalsRemoveExactlyTheirOwnBookmark is the regression for
// onAltM finding a bookmark's index under b.mu, unlocking, and removing by that
// index under a second lock: a concurrent removal in between shifted the
// slice, so the stale index deleted a neighbouring bookmark or sliced out of
// range. Every goroutine here removes a distinct line at once; exactly those
// bookmarks must go and the untouched one must survive.
func TestConcurrentRemovalsRemoveExactlyTheirOwnBookmark(t *testing.T) {
	const n = 50
	b := &Bookmarks{persistCh: make(chan []bookmark, 1)}
	done := make(chan struct{})
	go func() { // discard persisted snapshots instead of writing to disk
		for {
			select {
			case <-b.persistCh:
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	for i := 0; i <= n; i++ {
		b.bookmarks = append(b.bookmarks, bookmark{filePath: "/f.go", line: uint32(i), active: true})
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(line uint32) {
			defer wg.Done()
			if !b.removeBookmarkAt("/f.go", line) {
				t.Errorf("line %d: bookmark not found — removed by another goroutine's stale index?", line)
			}
		}(uint32(i))
	}
	wg.Wait()

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.bookmarks) != 1 || b.bookmarks[0].line != n {
		t.Fatalf("remaining = %+v, want only the bookmark at line %d", b.bookmarks, n)
	}
}
