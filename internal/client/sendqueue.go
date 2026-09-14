package client

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/document"
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
}

type queuedSend struct {
	seq         uint64
	bufID       uint32
	op          document.Op
	generation  uint64
	baseVersion uint64
}

// opAck records that the server applied one queued op, and at which version.
type opAck struct {
	seq     uint64
	version uint64
}

// opsAckedMsg reports the acknowledgements from one drain pass.
type opsAckedMsg struct {
	bufID uint32
	acks  []opAck
}

// RouteBufID implements RoutableMsg: acknowledgements must reach the buffer
// they belong to even when the user has switched tabs, or its pending queue
// never drains and every later op is rebased against ops the server already
// has.
func (m opsAckedMsg) RouteBufID() uint32 { return m.bufID }

// enqueue adds an op to the tail and reports whether the caller must start a
// drain. Only one drain runs at a time; an enqueue arriving during one is
// picked up by the drain already in progress.
func (q *sendQueue) enqueue(s queuedSend) (needsDrain bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queued = append(q.queued, s)
	if q.draining {
		return false
	}
	q.draining = true
	return true
}

// next pops the head, or reports done and clears the draining flag. Both happen
// under one lock hold: checking for emptiness and releasing the flag separately
// would let an op enqueued in between sit forever with nobody draining it.
func (q *sendQueue) next() (queuedSend, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queued) == 0 {
		q.draining = false
		return queuedSend{}, false
	}
	s := q.queued[0]
	q.queued = q.queued[1:]
	return s, true
}

// discard drops everything queued and stops the drain, for when the ops are no
// longer meaningful — a failed send that forces a resync, or a wholesale buffer
// swap. Their coordinates describe content the client is about to throw away.
func (q *sendQueue) discard() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queued = nil
	q.draining = false
}

// drainCmd sends queued ops in order, one round trip at a time, and reports the
// versions the server assigned them.
//
// It stops at the first failure rather than continuing. Later ops were computed
// against a document that includes the failed one, so sending them next would
// apply them at coordinates the server's content does not match — corrupting
// the buffer instead of merely failing to update it. The failure routes into
// the existing resync path, which is the only safe recovery.
func (m Model) drainCmd() tea.Cmd {
	q := m.sendQ
	rpc := m.rpc
	bufID := m.bufID
	return func() tea.Msg {
		var acks []opAck
		for {
			s, ok := q.next()
			if !ok {
				if len(acks) == 0 {
					return nil
				}
				return opsAckedMsg{bufID: bufID, acks: acks}
			}
			ctx, cancel := context.WithTimeout(context.Background(), applyOpTimeout)
			version, err := rpc.ApplyOp(ctx, s.bufID, s.op, s.generation, s.baseVersion)
			cancel()
			if err != nil {
				clientLog("ApplyOp FAILED buf=%d gen=%d base=%d op=%s: %v",
					s.bufID, s.generation, s.baseVersion, describeOp(s.op), err)
				q.discard()
				return applyOpFailedMsg{bufID: s.bufID, err: err}
			}
			acks = append(acks, opAck{seq: s.seq, version: version})
		}
	}
}
