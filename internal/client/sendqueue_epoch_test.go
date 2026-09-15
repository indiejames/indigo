package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

func testSend(seq uint64) queuedSend {
	return queuedSend{
		seq:   seq,
		bufID: 1,
		op:    document.Op{Type: document.OpInsert, InsertText: "x"},
	}
}

// TestDiscardRetiresARunningDrain is the ordering regression.
//
// discard clears the draining flag but cannot stop the goroutine that was
// draining — it is typically parked in ApplyOp. Without an epoch the next
// enqueue saw draining == false and started a second drain, so two goroutines
// popped from one queue and sent concurrently. Ordering is the entire purpose
// of this type: the server rebases an incoming op past other clients' ops but
// never past the sender's own, so an op arriving before one it was typed after
// is applied at coordinates that no longer mean anything.
//
// The sequence here is the real one: a drain is running, a resync discards, and
// the user keeps typing.
func TestDiscardRetiresARunningDrain(t *testing.T) {
	q := &sendQueue{}

	needsDrain, epoch := q.enqueue(testSend(1))
	if !needsDrain {
		t.Fatal("first enqueue must ask for a drain")
	}

	// The drain goroutine takes its first op and is now in flight.
	if _, ok := q.next(epoch); !ok {
		t.Fatal("drain got nothing to send")
	}

	// A resync discards while that goroutine is still parked in ApplyOp.
	q.discard()

	// The user types again. This legitimately starts a new drain.
	needsDrain2, epoch2 := q.enqueue(testSend(2))
	if !needsDrain2 {
		t.Fatal("after a discard, a new enqueue must start a drain")
	}
	if epoch2 == epoch {
		t.Fatal("discard must move the epoch on, or the old drain is still live")
	}

	// The old goroutine's ApplyOp returns and it loops for more work. It must
	// get nothing: taking seq 2 here would mean two goroutines sending
	// concurrently, in whatever order they happen to reach the socket.
	if s, ok := q.next(epoch); ok {
		t.Fatalf("a retired drain popped seq %d — two drains are now racing", s.seq)
	}

	// And the op the user just typed is still there for the drain that owns it.
	s, ok := q.next(epoch2)
	if !ok {
		t.Fatal("the new drain found nothing; the retired one must not consume its work")
	}
	if s.seq != 2 {
		t.Errorf("new drain popped seq %d, want 2", s.seq)
	}
}

// TestRetiredDrainDoesNotReportItsFailure covers the other half: a send that
// fails after a discard describes content the client has already thrown away,
// and reporting it starts a second resync against a buffer that has moved on.
func TestRetiredDrainDoesNotReportItsFailure(t *testing.T) {
	q := &sendQueue{}
	_, epoch := q.enqueue(testSend(1))
	if _, ok := q.next(epoch); !ok {
		t.Fatal("drain got nothing to send")
	}

	q.discard() // a resync, for some other reason, lands first

	if q.retire(epoch) {
		t.Error("a drain from a superseded epoch must not report its failure")
	}
}

// TestRetireFromTheLiveDrainStillReports is the complement: an ordinary failure
// with no discard in between must still reach the resync path.
func TestRetireFromTheLiveDrainStillReports(t *testing.T) {
	q := &sendQueue{}
	_, epoch := q.enqueue(testSend(1))
	if _, ok := q.next(epoch); !ok {
		t.Fatal("drain got nothing to send")
	}

	if !q.retire(epoch) {
		t.Fatal("the live drain must report its failure")
	}
	if !q.idle() {
		t.Error("retire must leave the queue idle, as discard does")
	}
}

// TestRetiredDrainDoesNotStrandTheNewOne guards the detail that makes next's
// stale branch correct: it must not touch the draining flag. Clearing it there
// would let a third enqueue start yet another drain while the second is still
// running, which is the same race again one step along.
func TestRetiredDrainDoesNotStrandTheNewOne(t *testing.T) {
	q := &sendQueue{}
	_, epoch := q.enqueue(testSend(1))
	q.next(epoch) //nolint:errcheck // in flight
	q.discard()

	_, epoch2 := q.enqueue(testSend(2))
	q.next(epoch) //nolint:errcheck // the retired drain looks for more work

	// The live drain is still the only one draining.
	if needsDrain, _ := q.enqueue(testSend(3)); needsDrain {
		t.Error("a retired drain's next() cleared the draining flag, so a third " +
			"enqueue started a competing drain")
	}
	if s, ok := q.next(epoch2); !ok || s.seq != 2 {
		t.Errorf("live drain popped %v/%v, want seq 2", s.seq, ok)
	}
}
