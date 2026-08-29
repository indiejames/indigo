package main

import (
	"sync"
	"testing"
)

// TestBookmarksNoRaceBetweenEditEventAndPersist is a regression test for a
// data race: onEditEvent mutates bookmark elements in place (bm.line,
// bm.active) while holding b.mu, but persistBookmarks used to be called on
// the live b.bookmarks slice *after* releasing the lock (both from onAltM's
// add/remove paths and from onEditEvent's own `go persistBookmarks(...)`).
// The plugin manager dispatches HandleKey (which drives onAltM) and
// DispatchEditEvent (which drives onEditEvent) as independent goroutines, so
// this is a genuine concurrent unsynchronized read/write, not theoretical:
// rapid line inserts/deletes racing an alt+m toggle could read a torn
// bookmark struct or race append's slice-header write, corrupting the
// persisted bookmarks.json or crashing under -race.
//
// The fix takes a snapshot (an independent copy) of b.bookmarks under the
// lock and persists *that*, so persistBookmarks never touches memory a
// concurrent mutation can still reach. Run with `go test -race`.
//
// Many bookmarks are seeded (rather than just one or two) so onEditEvent's
// mutation loop and persistBookmarks' read loop each run long enough to give
// the race detector a realistic chance to observe two goroutines touching
// the same element concurrently — a tiny dataset lets both loops finish
// before the other goroutine's next lock/unlock, hiding the race.
func TestBookmarksNoRaceBetweenEditEventAndPersist(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // bookmarksFilePath() must not touch the real user config dir

	const seeded = 500
	initial := make([]bookmark, seeded)
	for i := range initial {
		initial[i] = bookmark{filePath: "/tmp/a.go", line: uint32(i), active: true}
	}
	b := &Bookmarks{bookmarks: initial}

	const n = 200
	var wg sync.WaitGroup
	wg.Add(2)

	// Simulates a burst of edits to the bookmarked file — the real
	// onEditEvent, exercised exactly as DispatchEditEvent calls it. Alternates
	// insert/delete so bm.line and bm.active both get mutated repeatedly.
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if i%2 == 0 {
				b.onEditEvent(1, "/tmp/a.go", 0, 1)
			} else {
				b.onEditEvent(1, "/tmp/a.go", 0, -1)
			}
		}
	}()

	// Simulates the alt+m "add a bookmark" path's locked mutate + snapshot +
	// unlocked persist sequence (the same shape onAltM uses), without
	// needing a live sdk.Api for BufferInfo/ShowInputPrompt.
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			b.mu.Lock()
			b.bookmarks = append(b.bookmarks, bookmark{
				filePath: "/tmp/b.go",
				line:     uint32(i),
				active:   true,
			})
			snapshot := snapshotBookmarks(b.bookmarks)
			b.mu.Unlock()
			persistBookmarks(snapshot)
		}
	}()

	wg.Wait()
}
