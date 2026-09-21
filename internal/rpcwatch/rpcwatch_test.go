package rpcwatch

import (
	"context"
	"errors"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

// stubHook stands in for a real capability. Send hands back an answer the test
// resolves by hand, and Recv parks the delivered call where the test can reach
// its returner — so both directions can be held open for as long as an
// assertion needs, which is the state this package exists to notice.
type stubHook struct {
	ret       *capnp.StructReturner
	delivered chan capnp.Recv
	brand     capnp.Brand
}

func (s *stubHook) Send(_ context.Context, snd capnp.Send) (*capnp.Answer, capnp.ReleaseFunc) {
	return s.ret.Answer(snd.Method, nopPipeline{})
}

// nopPipeline satisfies the pipeline caller StructReturner requires to build
// an unresolved answer. Nothing in these tests pipelines off a pending call.
type nopPipeline struct{}

func (nopPipeline) PipelineSend(_ context.Context, _ []capnp.PipelineOp, s capnp.Send) (*capnp.Answer, capnp.ReleaseFunc) {
	return capnp.ErrorAnswer(s.Method, errors.New("no pipelining in this stub")), func() {}
}

func (nopPipeline) PipelineRecv(_ context.Context, _ []capnp.PipelineOp, r capnp.Recv) capnp.PipelineCaller {
	r.Reject(errors.New("no pipelining in this stub"))
	return nil
}

func (s *stubHook) Recv(_ context.Context, r capnp.Recv) capnp.PipelineCaller {
	s.delivered <- r
	return nil
}

func (s *stubHook) Brand() capnp.Brand { return s.brand }
func (s *stubHook) Shutdown()          {}
func (s *stubHook) String() string     { return "stubHook" }

type trackRecord struct {
	tag    string
	what   string
	budget time.Duration
}

// captureTracking swaps the hangdetect seam for the duration of a test and
// returns the channels the two halves report on.
func captureTracking(t *testing.T) (started chan trackRecord, finished chan string) {
	t.Helper()
	started = make(chan trackRecord, 8)
	finished = make(chan string, 8)
	old := track
	track = func(tag, what string, budget time.Duration) func() {
		started <- trackRecord{tag, what, budget}
		return func() { finished <- what }
	}
	t.Cleanup(func() { track = old })
	return started, finished
}

func openFileMethod() capnp.Method {
	return capnp.Method{
		InterfaceID:   0x1234,
		MethodID:      7,
		InterfaceName: "editor.capnp:EditorService",
		MethodName:    "openFile",
	}
}

func mustNotFinish(t *testing.T, finished chan string) {
	t.Helper()
	select {
	case what := <-finished:
		t.Fatalf("%s was retired while it was still outstanding — the whole point is that it stays tracked", what)
	case <-time.After(50 * time.Millisecond):
	}
}

func mustFinish(t *testing.T, finished chan string, want string) {
	t.Helper()
	select {
	case got := <-finished:
		if got != want {
			t.Errorf("retired %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was never retired; a call that did come back would be reported as stalled forever", want)
	}
}

func TestOutgoingCallStaysTrackedUntilTheAnswerResolves(t *testing.T) {
	started, finished := captureTracking(t)

	ret := new(capnp.StructReturner)
	wrapped := WrapOutgoing("client", capnp.NewClient(&stubHook{ret: ret}))
	defer wrapped.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ans, rel := wrapped.SendCall(ctx, capnp.Send{Method: openFileMethod()})
	defer rel()

	rec := <-started
	if rec.tag != "client" {
		t.Errorf("tag = %q, want client", rec.tag)
	}
	if rec.what != "rpc EditorService.openFile" {
		t.Errorf("what = %q, want the schema's own name for the method", rec.what)
	}
	// The budget follows the caller's deadline rather than a constant chosen
	// here — that is what makes one threshold fit call sites ranging from
	// 300ms to 30s.
	if rec.budget < 5*time.Second || rec.budget > 8*time.Second {
		t.Errorf("budget = %v, want the caller's 5s deadline plus grace", rec.budget)
	}

	mustNotFinish(t, finished)

	ret.PrepareReturn(nil)
	ret.Return()
	mustFinish(t, finished, "rpc EditorService.openFile")

	if _, err := ans.Struct(); err != nil {
		t.Errorf("wrapping changed the result the caller sees: %v", err)
	}
}

// TestOutgoingCallErrorStillReachesTheCaller guards the thing that would make
// this package worse than useless: a diagnostic that swallows a failure.
func TestOutgoingCallErrorStillReachesTheCaller(t *testing.T) {
	_, finished := captureTracking(t)

	ret := new(capnp.StructReturner)
	wrapped := WrapOutgoing("client", capnp.NewClient(&stubHook{ret: ret}))
	defer wrapped.Release()

	ans, rel := wrapped.SendCall(context.Background(), capnp.Send{Method: openFileMethod()})
	defer rel()

	want := errors.New("server said no")
	ret.PrepareReturn(want)
	ret.Return()

	if _, err := ans.Struct(); err == nil {
		t.Fatal("the error did not reach the caller")
	}
	// A failed call is still a call that came back, so it must be retired.
	mustFinish(t, finished, "rpc EditorService.openFile")
}

func TestIncomingCallStaysTrackedUntilItReturns(t *testing.T) {
	started, finished := captureTracking(t)

	stub := &stubHook{delivered: make(chan capnp.Recv, 1)}
	wrapped := WrapIncoming("server", capnp.NewClient(stub), 20*time.Second)
	defer wrapped.Release()

	outer := new(capnp.StructReturner)
	wrapped.RecvCall(context.Background(), capnp.Recv{
		Method:      openFileMethod(),
		Returner:    outer,
		ReleaseArgs: func() {},
	})

	rec := <-started
	if rec.tag != "server" || rec.what != "rpc EditorService.openFile" {
		t.Errorf("tracked %q under tag %q, want the method name under server", rec.what, rec.tag)
	}
	// Fixed rather than context-derived: a server handler has no caller
	// deadline to read, and the measurement deliberately includes time spent
	// queued behind an earlier handler.
	if rec.budget != 20*time.Second {
		t.Errorf("budget = %v, want the budget passed to WrapIncoming", rec.budget)
	}

	delivered := <-stub.delivered
	mustNotFinish(t, finished)

	// The handler returns through the wrapper that was substituted in, and
	// only then is the call retired.
	delivered.Returner.PrepareReturn(nil)
	delivered.Returner.Return()
	mustFinish(t, finished, "rpc EditorService.openFile")
}

// TestIncomingRejectionRetiresTheCall covers the other way out of a handler.
// Recv.Reject goes through PrepareReturn/Return like a success does, and a
// path that skipped retirement would leave a rejected call looking stalled.
func TestIncomingRejectionRetiresTheCall(t *testing.T) {
	_, finished := captureTracking(t)

	stub := &stubHook{delivered: make(chan capnp.Recv, 1)}
	wrapped := WrapIncoming("server", capnp.NewClient(stub), time.Second)
	defer wrapped.Release()

	wrapped.RecvCall(context.Background(), capnp.Recv{
		Method:      openFileMethod(),
		Returner:    new(capnp.StructReturner),
		ReleaseArgs: func() {},
	})
	delivered := <-stub.delivered
	delivered.Reject(errors.New("unimplemented"))
	mustFinish(t, finished, "rpc EditorService.openFile")
}

// TestBrandIsDelegated pins the one property a wrapper is most likely to get
// wrong quietly: a capability that misreports its identity works until the day
// something asks.
func TestBrandIsDelegated(t *testing.T) {
	brand := capnp.Brand{Value: "the-real-thing"}
	inner := capnp.NewClient(&stubHook{ret: new(capnp.StructReturner), brand: brand})

	for name, wrapped := range map[string]capnp.Client{
		"outgoing": WrapOutgoing("client", inner.AddRef()),
		"incoming": WrapIncoming("server", inner.AddRef(), time.Second),
	} {
		snap := wrapped.Snapshot()
		if got := snap.Brand(); got != brand {
			t.Errorf("%s brand = %v, want the inner capability's %v", name, got, brand)
		}
		snap.Release()
		wrapped.Release()
	}
	inner.Release()
}

func TestCallNameFallsBackWhenTheSchemaNamesAreMissing(t *testing.T) {
	// A peer built from a schema this binary does not have still has to be
	// reportable; the numeric form is the only thing left.
	got := callName(capnp.Method{InterfaceID: 0xabc, MethodID: 3})
	if got == "rpc " || got == "rpc ." {
		t.Errorf("callName produced an empty name: %q", got)
	}
	if got := callName(capnp.Method{MethodName: "applyOp"}); got != "rpc applyOp" {
		t.Errorf("callName = %q, want %q", got, "rpc applyOp")
	}
	if got := callName(openFileMethod()); got != "rpc EditorService.openFile" {
		t.Errorf("callName = %q; the schema file prefix should be dropped", got)
	}
}
