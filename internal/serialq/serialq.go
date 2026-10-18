// Package serialq runs jobs one at a time, in the order they were pushed,
// without the pusher waiting for them.
//
// It exists for notifications whose order matters and whose sender cannot
// block: a plugin's edit events, which have to arrive in the order the buffer
// applied the edits, and are queued while the server holds its buffer lock.
// One goroutine per notification delivered them in whatever order the
// scheduler chose, so two quick edits could reach a plugin reversed and the
// positions it tracks drifted.
package serialq

import "sync"

// Queue is an ordered executor. The zero value is ready to use.
type Queue struct {
	mu      sync.Mutex
	jobs    []func()
	running bool
}

// Push queues job to run after every job pushed before it. It never blocks:
// the jobs run on a goroutine the queue starts when it has work and that exits
// when it has none.
func (q *Queue) Push(job func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	if !q.running {
		q.running = true
		go q.drain()
	}
}

func (q *Queue) drain() {
	for {
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.running = false
			q.mu.Unlock()
			return
		}
		job := q.jobs[0]
		q.jobs[0] = nil
		q.jobs = q.jobs[1:]
		q.mu.Unlock()
		job()
	}
}
