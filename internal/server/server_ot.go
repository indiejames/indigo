package server

import "github.com/indiejames/indigo/internal/document"

// This file holds the server half of indigo's operational transform, in the
// Jupiter arrangement (Nichols et al., 1995) — the right fit because the server
// already orders every op, which is exactly Jupiter's topology.
//
// The consequence of a central serializer is that only TP1 is required of
// document.Transform; TP2, the genuinely hard property, applies to peer-to-peer
// convergence and not to this. See document/transform.go.
//
// The server keeps one outgoing queue per client per buffer (bufferEntry.
// outgoing). Receiving an op from client C is:
//
//  1. drop from C's queue everything C's baseVersion says it has now seen;
//  2. rebase the incoming op past what is left, and rewrite that remainder to
//     account for the incoming op;
//  3. apply the rebased op;
//  4. append it to every *other* client's queue.
//
// Step 2 rewriting the queue is the part a shared history cannot express, and
// the reason bufferEntry.outgoing exists.

// serverOrderWins is the tie-break passed to document.Transform when two
// concurrent inserts land on the same position and nothing in the content
// decides the order.
//
// The rule is "whichever op the server ordered first wins", which every
// participant can agree on without extra state: on the server an already-queued
// op was applied before the one arriving now, so the incoming op loses; on the
// client a remote op reached the server before that client's still-unacked ops,
// so the remote op wins. Deriving the tie-break from arrival order at each end
// instead would let the two sides disagree — precisely the divergence this
// machinery exists to prevent.
const incomingOpLosesTies = false

// dropAcknowledged removes from a client's queue every op it has confirmed
// seeing, and reports what remains. Ops carry the server version they were
// applied at, which survives any later rebasing, so the comparison stays valid
// however much the queued op itself was rewritten.
func dropAcknowledged(queue []document.Op, ackedVersion uint64) []document.Op {
	keep := queue[:0:0]
	for _, op := range queue {
		if op.Version > ackedVersion {
			keep = append(keep, op)
		}
	}
	return keep
}

// rebaseIncoming prepares ops arriving from clientID for application.
//
// It returns the ops to actually apply — rebased past everything that client
// has not yet seen — and stores the correspondingly rewritten queue back on the
// entry. Callers must hold s.mu.
//
// The returned slice can be longer than the input (a delete splitting around a
// concurrent insert) or shorter, including empty when every character an op
// meant to delete was already deleted by someone else. An empty result is a
// normal outcome, not an error: the op's intent was already satisfied.
func rebaseIncoming(entry *bufferEntry, clientID uint64, baseVersion uint64, ops []document.Op) []document.Op {
	if entry.outgoing == nil {
		entry.outgoing = make(map[uint64][]document.Op)
	}
	pending := dropAcknowledged(entry.outgoing[clientID], baseVersion)
	if len(pending) == 0 {
		entry.outgoing[clientID] = nil
		return ops
	}
	rebased, rewritten := document.TransformSeq(ops, pending, incomingOpLosesTies)
	entry.outgoing[clientID] = rewritten
	return rebased
}

// broadcast appends applied ops to every client's queue except the one that
// sent them, which already has them locally. Callers must hold s.mu.
//
// The ops are in the server's current document context, which is by
// construction the context each other client's queue ends in — their queue is
// exactly the gap between what they last acknowledged and now — so they can be
// appended as-is rather than rebased again.
func broadcast(entry *bufferEntry, fromClientID uint64, ops []document.Op) {
	if len(ops) == 0 {
		return
	}
	if entry.outgoing == nil {
		entry.outgoing = make(map[uint64][]document.Op)
	}
	for clientID := range entry.clients {
		if clientID == fromClientID {
			continue
		}
		entry.outgoing[clientID] = append(entry.outgoing[clientID], ops...)
	}
}

// resetOutgoing clears every client's queue, for when the buffer object is
// replaced wholesale (format-on-save, SaveAs, DiscardRecovery, Format, reload).
// Queued ops describe the old buffer and cannot be rebased onto the new one;
// clients learn about the swap from the generation bump and resync instead.
// Callers must hold s.mu.
func resetOutgoing(entry *bufferEntry) {
	entry.outgoing = nil
}

// applyServerOriginated applies ops that did not arrive through a client's
// ApplyOp — a plugin edit, a cross-file move, a workspace edit — and queues them
// for delivery. It returns the buffer's resulting version. Callers must hold
// s.mu.
//
// Every such site must go through here. Under the previous shared-history
// delivery these ops reached clients implicitly, because GetUpdates read the
// buffer's history and every Apply landed there. Per-client queues are fed
// explicitly, so a site that applies straight to the buffer and skips this is
// invisible to every window: the edit exists in the server's buffer and in
// nobody's view of it, with no error and nothing to notice.
//
// excludeClientID is not delivered to, matching ApplyOp's rule that a client
// already has what it sent. Pass the reserved pluginClientID (0, never a real
// client) to deliver to everyone.
func recordAppliedFrom(entry *bufferEntry, clientID uint64, n int) {
	if n == 0 {
		return
	}
	if entry.appliedFromClient == nil {
		entry.appliedFromClient = make(map[uint64]uint64)
	}
	// Counted in ops the client *sent*, not ops applied: rebasing can split one
	// into two or cancel it entirely, and the client counts what it sent.
	entry.appliedFromClient[clientID] += uint64(n)
}

func applyServerOriginated(entry *bufferEntry, excludeClientID uint64, ops ...document.Op) uint64 {
	applied := make([]document.Op, 0, len(ops))
	version := entry.buf.Version()
	for _, op := range ops {
		version = entry.buf.Apply(op)
		op.Version = version
		applied = append(applied, op)
	}
	broadcast(entry, excludeClientID, applied)
	return version
}
