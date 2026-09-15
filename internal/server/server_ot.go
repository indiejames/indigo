package server

import (
	"fmt"

	"github.com/indiejames/indigo/internal/document"
)

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
func rebaseIncoming(entry *bufferEntry, clientID uint64, baseVersion uint64, ops []document.Op) ([]document.Op, error) {
	// An op based on a version below what this client has already acknowledged
	// cannot be rebased: the ops it would need to be rebased past were dropped
	// from its queue when it acknowledged them. Applying it anyway is silent
	// corruption — coordinates describing content the server no longer has —
	// so it is refused instead, and the caller resyncs.
	//
	// A well-behaved client cannot produce this: it only polls when its send
	// queue is idle, so it never acknowledges past an op it has not sent. This
	// guards everything else — a plugin, an agent tool, a future change to that
	// rule — and turns the one remaining way to corrupt a buffer quietly into
	// the visible fallback that exists for exactly this.
	if pruned, ok := entry.prunedThrough[clientID]; ok && baseVersion < pruned {
		return nil, fmt.Errorf("base version %d is older than the acknowledged version %d; "+
			"the ops needed to rebase it are no longer retained", baseVersion, pruned)
	}
	if entry.outgoing == nil {
		entry.outgoing = make(map[uint64][]document.Op)
	}
	pending := dropAcknowledged(entry.outgoing[clientID], baseVersion)
	if len(pending) == 0 {
		entry.outgoing[clientID] = nil
		return ops, nil
	}
	rebased, rewritten := document.TransformSeq(ops, pending, incomingOpLosesTies)
	entry.outgoing[clientID] = rewritten
	return rebased, nil
}

// recordPruned notes how far a client has acknowledged, which is how far its
// outgoing queue has been discarded. Monotonic: a poll reporting an older
// version than one already seen must not widen what the server claims it can
// still rebase against. Callers must hold s.mu.
func recordPruned(entry *bufferEntry, clientID, since uint64) {
	if entry.prunedThrough == nil {
		entry.prunedThrough = make(map[uint64]uint64)
	}
	if since > entry.prunedThrough[clientID] {
		entry.prunedThrough[clientID] = since
	}
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

// resetOutgoing clears every client's queue and per-client watermarks, for when
// the buffer object is replaced wholesale (format-on-save, SaveAs,
// DiscardRecovery, Format, reload). Callers must hold s.mu.
//
// The queues go because their ops describe the old buffer and cannot be rebased
// onto the new one; clients learn of the swap from the generation bump and
// resync instead.
//
// The watermarks go for a sharper reason. They are counted in the *replaced*
// buffer's version space, and the new buffer restarts at version 0 — so a
// client that resyncs and edits, legitimately based on version 0, looks to
// prunedThrough like it is working from something older than it already
// acknowledged, and is refused. It resyncs, tries again, is refused again: the
// buffer becomes permanently unwritable. Clearing them is what keeps the
// stale-base guard from firing on the one event that is supposed to reset
// everything.
func resetOutgoing(entry *bufferEntry) {
	entry.outgoing = nil
	entry.prunedThrough = nil
	entry.appliedFromClient = nil
	// sinceByClient is counted in the replaced buffer's version space too. It
	// feeds TrimHistory's watermark rather than the rebase guard, so a stale
	// value here fails the other way round: too *high*, so it stops holding
	// history back and lets ops be reclaimed that a client which has not polled
	// since the swap still needs. Clearing it is conservative — an absent entry
	// reads as 0, which blocks trimming until every client reports progress
	// against the new buffer.
	entry.sinceByClient = nil
}

// verifyExpectedText checks that a batch carrying expectations still means what
// its sender meant, comparing what it asked for against what rebasing left.
// Callers must hold s.mu.
//
// The thing this guards is narrower than it first appears, and worth stating
// precisely. Rebasing already protects *placement*: an op sent with the
// baseVersion its coordinates were read at lands on the text it described, even
// if the document moved underneath. What rebasing cannot protect is *intent*. If
// another client already deleted that text, the op correctly transforms to
// nothing; if they edited part of it, the op correctly splits around their
// change. Both are right as transforms and both leave the caller's "replace this
// exact text" silently unfulfilled — the reply says success and the edit did not
// happen, or happened to half of it.
//
// So each expectation must survive rebasing as exactly one delete still covering
// exactly that text. Anything else is reported rather than applied:
//
//   - no surviving delete: someone else removed the text already;
//   - more than one: someone else edited inside it, splitting the delete;
//   - different text at the range: the range survived but its contents did not.
//
// Checked after rebasing and before applying. After, because the original
// coordinates describe a document the server may have moved past. Before,
// because there is no rollback: a check interleaved with the applies could
// refuse a batch it had already half-written.
func verifyExpectedText(buf *document.Buffer, input, rebased []document.Op) error {
	// Counted per distinct expectation text rather than compared one-to-one.
	// Transform carries ExpectText onto both halves of a split, which is what
	// makes "more than one survivor" mean "someone edited inside it" — but a
	// batch carrying the same expectation twice (two occurrences of one string)
	// would then see both its survivors against each expectation and be
	// rejected as a split, refusing work that is entirely valid.
	//
	// Comparing counts distinguishes the cases that actually arise: as many
	// survivors as expectations means each one came through whole. It does not
	// make the match unambiguous — one expectation cancelled while another
	// splits leaves the counts equal — and closing that needs an identity on
	// Op, carried through the transform and across the wire. Not worth a schema
	// change for a case no caller can currently produce: every batch that sets
	// ExpectText today carries exactly one delete.
	wantCount := make(map[string]int)
	for _, want := range input {
		if want.Type == document.OpDelete && want.ExpectText != "" {
			wantCount[want.ExpectText]++
		}
	}
	seen := make(map[string]bool)
	for _, want := range input {
		if want.Type != document.OpDelete || want.ExpectText == "" {
			continue
		}
		if seen[want.ExpectText] {
			continue // already checked every survivor for this text
		}
		seen[want.ExpectText] = true
		var survivors []document.Op
		for _, op := range rebased {
			if op.Type == document.OpDelete && op.ExpectText == want.ExpectText {
				survivors = append(survivors, op)
			}
		}
		switch {
		case len(survivors) == 0:
			return fmt.Errorf("text %q is no longer present: another client removed it "+
				"since it was read", truncateForError(want.ExpectText))
		case len(survivors) < wantCount[want.ExpectText]:
			return fmt.Errorf("text %q is no longer present at every place this edit "+
				"expected it: another client removed some of them since it was read",
				truncateForError(want.ExpectText))
		case len(survivors) > wantCount[want.ExpectText]:
			return fmt.Errorf("text %q was edited by another client since it was read, "+
				"so this edit would apply to only part of it", truncateForError(want.ExpectText))
		}
		// Every survivor is checked, not just the first: with more than one
		// expectation of this text there is more than one range to confirm.
		for _, op := range survivors {
			if err := verifyRangeText(buf, op); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyRangeText confirms the text actually at op's range is what op expected.
func verifyRangeText(buf *document.Buffer, op document.Op) error {
	actual, err := extractRange(buf, op.FromLine, op.FromCol, op.ToLine, op.ToCol)
	if err != nil {
		return fmt.Errorf("cannot verify expected text at %d:%d-%d:%d: %w",
			op.FromLine, op.FromCol, op.ToLine, op.ToCol, err)
	}
	if actual != op.ExpectText {
		// Both texts are reported, truncated: the caller's recovery is to
		// re-read and recompute, and knowing what is actually there tells it
		// whether that is worth doing or whether its premise is stale.
		return fmt.Errorf("text at %d:%d-%d:%d has changed since it was read: expected %q, found %q",
			op.FromLine, op.FromCol, op.ToLine, op.ToCol,
			truncateForError(op.ExpectText), truncateForError(actual))
	}
	return nil
}

// truncateForError shortens text for an error message. These strings are buffer
// content and can be arbitrarily long; an error line is not the place for a
// whole paste.
func truncateForError(s string) string {
	const max = 60
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
