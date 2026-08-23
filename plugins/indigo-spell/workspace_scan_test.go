package main

import (
	"sort"
	"sync"
	"testing"
	"time"
)

func TestPathsToClear(t *testing.T) {
	prev := map[string]struct{}{"a.go": {}, "b.go": {}, "c.go": {}}
	found := map[string]struct{}{"a.go": {}}

	got := pathsToClear(prev, found)
	sort.Strings(got)
	want := []string{"b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("pathsToClear = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pathsToClear = %v, want %v", got, want)
			break
		}
	}
}

func TestPathsToClearNothingStale(t *testing.T) {
	prev := map[string]struct{}{"a.go": {}}
	found := map[string]struct{}{"a.go": {}, "b.go": {}}
	if got := pathsToClear(prev, found); len(got) != 0 {
		t.Errorf("pathsToClear = %v, want empty (nothing dropped out of found)", got)
	}
}

// TestRunScanSerializedCoalescesOverlappingRequests is a regression test:
// runScanSerialized must not let two scans walk concurrently (which could
// let an older, slower run's results land after a newer run's for the same
// path). Firing several overlapping requests while one is in flight must
// coalesce into exactly one rerun, not one rerun per overlapping request
// and not zero (silently dropping legitimate rescan requests).
func TestRunScanSerializedCoalescesOverlappingRequests(t *testing.T) {
	s := newTestSpell()

	var mu sync.Mutex
	runs := 0
	started := make(chan struct{}, 1)
	proceed := make(chan struct{})

	scanOnce := func() {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		if n == 1 {
			started <- struct{}{}
			<-proceed // block the first run until the test releases it
		}
	}

	done := make(chan struct{})
	go func() {
		s.runScanSerialized(scanOnce)
		close(done)
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first scan never started")
	}

	// Fire several overlapping requests while the first run is still
	// blocked; each should coalesce (set rescanPending) rather than run.
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runScanSerialized(scanOnce)
		}()
	}
	wg.Wait()

	close(proceed) // let the first (blocked) run finish

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runScanSerialized never finished")
	}

	mu.Lock()
	got := runs
	mu.Unlock()
	if got != 2 {
		t.Errorf("runs = %d, want exactly 2 (the original run plus one coalesced rerun for all 5 overlapping requests)", got)
	}

	s.mu.Lock()
	scanning, pending := s.scanning, s.rescanPending
	s.mu.Unlock()
	if scanning || pending {
		t.Errorf("scanning=%v rescanPending=%v after completion, want both false", scanning, pending)
	}
}
