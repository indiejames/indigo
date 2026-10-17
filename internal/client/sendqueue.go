package client

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/syncevent"
)

// sendQueue serialises this client's outbound edits per buffer.
//
// Ordering is not a nicety under operational transform, it is load-bearing.
// Every edit used to become its own tea.Cmd, and Bubble Tea runs commands in
// separate goroutines, so two keystrokes could reach the server in either
// order. That was survivable when the server applied ops verbatim. It is not
// survivable now: the server rebases an incoming op past other clients' ops but
// never past the sender's own, because the sender already had those locally —
// so an op arriving before one it was actually typed after gets applied without
// accounting for it, at coordinates that no longer mean anything.
//
// Enqueueing happens synchronously inside Update, which is single-threaded, so
// the queue order is exactly the order the user typed in. Draining is done by
// one in-flight command at a time rather than a long-lived goroutine, so there
// is nothing to shut down when a buffer closes.
type sendQueue struct {
	mu       sync.Mutex
	queued   []queuedSend
	draining bool
	// epoch invalidates a drain that is still running when discard happens.
	//
	// discard clears the draining flag, but it cannot stop the goroutine that
	// was draining — that goroutine is typically parked in ApplyOp. Without an
	// epoch the next enqueue sees draining == false, starts a second drain, and
	// two goroutines then pop from one queue and send concurrently. That
	// destroys the ordering this whole type exists to provide, and it happens
	// on exactly the paths discard is for: a failed send forcing a resync, or a
	// wholesale buffer swap, both of which are immediately followed by new
	// edits. A drain compares the epoch it started in before every step and
	// stops as soon as it is stale.
	epoch uint64
	// inflight is set from the moment a drain pops an op until its ApplyOp
	// returns, and quiet is closed when it clears. A resync waits on it: an op
	// popped before the resync began is still sent, and the snapshot has to be
	// taken after the server has dealt with it, not before.
	inflight bool
	quiet    chan struct{}
	// paused holds new edits in the queue without sending them, between a
	// resync starting and its snapshot being adopted. An edit sent in that
	// window could reach the server after the snapshot was taken, and the
	// client would never see it: the server does not echo a sender's own ops,
	// and the snapshot the client adopts predates it.
	paused bool
}

type queuedSend struct {
	// barrier, when set, makes this entry a marker rather than an op: it is
	// sent true once every op queued ahead of it has reached the server, or
	// false if they were discarded instead. See barrier().
	barrier     chan bool
	seq         uint64
	bufID       uint32
	op          document.Op
	generation  uint64
	baseVersion uint64
}

// enqueue adds an op to the tail and reports whether the caller must start a
// drain. Only one drain runs at a time; an enqueue arriving during one is
// picked up by the drain already in progress.
func (q *sendQueue) enqueue(s queuedSend) (needsDrain bool, epoch uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queued = append(q.queued, s)
	if q.draining || q.paused {
		return false, q.epoch
	}
	q.draining = true
	return true, q.epoch
}

// next pops the head, or reports done and clears the draining flag. Both happen
// under one lock hold: checking for emptiness and releasing the flag separately
// would let an op enqueued in between sit forever with nobody draining it.
// A drain from a superseded epoch gets nothing and does not touch the flag: the
// epoch it belonged to is over, and the drain that replaced it owns draining
// now. Clearing the flag here would strand whatever that newer drain has queued.
func (q *sendQueue) next(epoch uint64) (queuedSend, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if epoch != q.epoch {
		return queuedSend{}, false
	}
	// Barriers at the head are released here, under the lock: reaching one
	// means every op ahead of it has completed, because next is only called
	// again after the previous op's ApplyOp returned successfully.
	for len(q.queued) > 0 && q.queued[0].barrier != nil {
		q.queued[0].barrier <- true
		q.queued = q.queued[1:]
	}
	if len(q.queued) == 0 {
		q.draining = false
		return queuedSend{}, false
	}
	s := q.queued[0]
	q.queued = q.queued[1:]
	q.inflight = true
	q.quiet = make(chan struct{})
	return s, true
}

// sent marks the op the drain popped last as finished, successfully or not.
func (q *sendQueue) sent() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.inflight {
		q.inflight = false
		close(q.quiet)
	}
}

// idle reports that nothing is queued and no drain is running.
//
// Polling is gated on this, and the reason is subtle enough to be worth stating
// where it is enforced. An op carries the baseVersion its coordinates were
// computed against, and the server rebases it past everything applied since —
// but the server prunes a client's outgoing queue at whatever sinceVersion that
// client's last poll reported. Poll first and the queue is pruned past the very
// ops a still-unsent edit needed rebasing against, and the server applies it at
// coordinates that no longer mean anything. Sending first keeps the two in step.
func (q *sendQueue) idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queued) == 0 && !q.draining && !q.paused
}

// discard drops everything queued and stops the drain, for when the ops are no
// longer meaningful — a failed send that forces a resync, or a wholesale buffer
// swap. Their coordinates describe content the client is about to throw away.
//
// It also ends a pause (see pauseForResync): whatever was held is dropped with
// everything else, and sending resumes for edits made from here on.
func (q *sendQueue) discard() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropQueuedLocked()
	q.paused = false
	q.draining = false
	// Retires any drain still running. It cannot be stopped directly — it is
	// probably parked in ApplyOp — so it is made to recognise itself as stale
	// instead, before it can send another op or report a failure that belongs
	// to content the client has already thrown away.
	q.epoch++
}

// currentEpoch reports the live epoch, for a caller that drains without having
// just enqueued — the test harness, which flushes the shared queue directly
// rather than through the command an enqueue handed back.
func (q *sendQueue) currentEpoch() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.epoch
}

// retire is discard, but only if epoch is still the current one. It reports
// whether it acted, which is how a failing drain learns whether the failure it
// is holding still refers to anything the client cares about.
func (q *sendQueue) retire(epoch uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if epoch != q.epoch {
		return false
	}
	q.dropQueuedLocked()
	q.draining = false
	q.epoch++
	return true
}

// dropQueuedLocked empties the queue, telling any barrier in it that the ops
// it was waiting for will never be sent. q.mu must be held.
func (q *sendQueue) dropQueuedLocked() {
	for _, s := range q.queued {
		if s.barrier != nil {
			s.barrier <- false
		}
	}
	q.queued = nil
}

// barrier returns a channel that receives true once every op queued so far
// has reached the server, or false if they are discarded first (a failed send,
// a resync, a buffer swap). Ops queued after the call do not hold it up.
//
// Save, SaveAs and Format go through this. Each is its own command, so without
// it the request could overtake edits still waiting in the queue: a save would
// write the file without the last keystrokes, and :wq would then close the
// buffer and lose them outright.
func (q *sendQueue) barrier() <-chan bool {
	ch := make(chan bool, 1)
	if q == nil {
		ch <- true
		return ch
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queued) == 0 && !q.draining && !q.paused {
		ch <- true
		return ch
	}
	// Either a drain is running and will reach this entry, or the queue is
	// paused for a resync whose discard will release it with false.
	q.queued = append(q.queued, queuedSend{barrier: ch})
	return ch
}

// pauseForResync discards everything queued, retires the running drain, and
// holds edits made from now on until the resync's snapshot has been adopted
// (bufferResyncMsg, which calls discard). Called synchronously from Update, so
// no edit typed after the resync was decided on can be sent ahead of it.
func (q *sendQueue) pauseForResync() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropQueuedLocked()
	q.draining = false
	q.paused = true
	q.epoch++
}

// resume ends a pause without discarding what it held, and reports whether a
// drain must be started for it. For a resync that failed: there is no snapshot
// to adopt, so the held edits are sent as they would have been.
func (q *sendQueue) resume() (needsDrain bool, epoch uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.paused = false
	if q.draining || len(q.queued) == 0 {
		return false, q.epoch
	}
	q.draining = true
	return true, q.epoch
}

// waitQuiet blocks until no op is in flight, or ctx ends. After
// pauseForResync nothing new can be popped, so once this returns the server
// has finished with every op this client will send before the snapshot.
func (q *sendQueue) waitQuiet(ctx context.Context) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if !q.inflight {
		q.mu.Unlock()
		return nil
	}
	ch := q.quiet
	q.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// drainCmd sends queued ops in order, one round trip at a time.
//
// Success reports nothing. Which ops the server has taken is learned from the
// next poll instead, so that fact and the ops delivered alongside it come from
// one consistent view — see dropAckedBySeq.
//
// It stops at the first failure rather than continuing. Later ops were computed
// against a document that includes the failed one, so sending them next would
// apply them at coordinates the server's content does not match — corrupting
// the buffer instead of merely failing to update it. The failure routes into
// the existing resync path, which is the only safe recovery.
func (m Model) drainCmd(epoch uint64) tea.Cmd {
	q := m.sendQ
	rpc := m.rpc
	// queuedSend carries no path — the server is addressed by buffer id — so it
	// is captured here, or a path-filtered query cannot find send failures.
	path := m.filePath
	return func() tea.Msg {
		for {
			s, ok := q.next(epoch)
			if !ok {
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), applyOpTimeout)
			_, err := rpc.ApplyOp(ctx, s.bufID, s.op, s.generation, s.baseVersion)
			cancel()
			q.sent()
			if err != nil {
				clientLog("ApplyOp FAILED buf=%d gen=%d base=%d op=%s: %v",
					s.bufID, s.generation, s.baseVersion, describeOp(s.op), err)
				// A failure from a superseded epoch is dropped rather than
				// reported. Its op described content the client has already
				// discarded, and the resync that discarded it is either done or
				// under way — reporting it would start a second one, against a
				// buffer that has moved on.
				if !q.retire(epoch) {
					return nil
				}
				// Recorded only once the failure is one we are acting on. The
				// clientLog line above keeps the detail either way; what the
				// event stream must not show is a send_failed *after* the
				// resync_started that already explains it, which reads as a new
				// problem rather than a consequence of the old one.
				syncevent.Recordf("client", syncevent.SendFailed, s.bufID, path,
					"gen=%d base=%d op=%s: %v", s.generation, s.baseVersion, describeOp(s.op), err)
				return applyOpFailedMsg{bufID: s.bufID, err: err}
			}
		}
	}
}
