package serialq

import (
	"sync"
	"testing"
	"time"
)

// TestJobsRunInPushOrder pushes from one goroutine while earlier jobs are still
// running; the order they ran in must be the order they were pushed.
func TestJobsRunInPushOrder(t *testing.T) {
	var q Queue
	var mu sync.Mutex
	var got []int
	done := make(chan struct{})
	const n = 1000
	for i := range n {
		q.Push(func() {
			if i%100 == 0 {
				time.Sleep(time.Millisecond) // let pushes pile up behind a slow job
			}
			mu.Lock()
			got = append(got, i)
			mu.Unlock()
			if i == n-1 {
				close(done)
			}
		})
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the queue never ran its last job")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, v := range got {
		if v != i {
			t.Fatalf("job %d ran at position %d", v, i)
		}
	}
}

// TestPushNeverWaitsForAJob is what makes it safe to push under a lock.
func TestPushNeverWaitsForAJob(t *testing.T) {
	var q Queue
	release := make(chan struct{})
	defer close(release)
	q.Push(func() { <-release })
	pushed := make(chan struct{})
	go func() {
		q.Push(func() {})
		close(pushed)
	}()
	select {
	case <-pushed:
	case <-time.After(5 * time.Second):
		t.Fatal("Push blocked behind a running job")
	}
}

// TestQueueRestartsAfterDraining checks that a queue whose goroutine exited
// starts another for the next job.
func TestQueueRestartsAfterDraining(t *testing.T) {
	var q Queue
	for round := range 3 {
		ran := make(chan struct{})
		q.Push(func() { close(ran) })
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the job never ran", round)
		}
		for deadline := time.Now().Add(5 * time.Second); ; {
			q.mu.Lock()
			idle := !q.running
			q.mu.Unlock()
			if idle {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the queue never went idle")
			}
			time.Sleep(time.Millisecond)
		}
	}
}
