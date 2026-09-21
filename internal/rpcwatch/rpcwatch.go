// Package rpcwatch puts every Cap'n Proto call on a connection under
// hangdetect, without touching the fifty-odd call sites that make them.
//
// Doing this generically rather than per method is not only about typing. A
// call site instrumented by hand is one a later call site can be added without
// — and the call that stalls is, by definition, the one nobody expected to.
// Wrapping the capability means a method added to the schema tomorrow is
// covered the day it is added, and covered under the name the schema gives it.
//
// The two directions are watched differently, and are worth different things.
//
// Incoming (the server) is where the unbounded wait actually lives. A handler
// has no deadline of its own, so one that blocks blocks for as long as
// whatever it is blocked on takes — and capnp serialises a connection's calls
// until a handler releases the queue with call.Go(), so it takes every later
// call on that connection down with it. The clock therefore starts when the
// call is *received*, not when its handler begins: that queueing delay is the
// latency the client experiences and the thing head-of-line blocking is made
// of. The budget is fixed, since there is no caller deadline to read.
//
// Outgoing (the client) rarely fires, and that is measured rather than
// assumed. Over a real socket the RPC layer does resolve a question when the
// caller's context expires — a call against a handler parked forever in a FIFO
// read still returns "context deadline exceeded" on the dot. (The note in
// CLAUDE.md about a cancelled context failing to resolve a future is about an
// in-process fake capability, and says so.) So the budget here, the caller's
// own deadline plus a grace period, is mostly a backstop. It earns its place
// for two other reasons: the handshake calls in Dial carry no deadline at all,
// and a window that never opens is a hang like any other; and every stall
// report carries the in-flight inventory, which is what separates "frozen
// doing local work" from "frozen waiting on the server".
package rpcwatch

import (
	"context"
	"sync"
	"time"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/server"

	"github.com/indiejames/indigo/internal/hangdetect"
)

// track is the seam the tests swap. Production always uses the process-wide
// detector: a per-connection one would report each connection's stalls in
// isolation, and the inventory that makes a report readable is precisely the
// one that spans them.
var track = hangdetect.Track

// WrapOutgoing returns a capability that behaves exactly like c while
// reporting any call on it that outlives its context's deadline.
//
// c must be a capability this process only calls, never one it hands to a
// peer: the wrapper has its own identity, so a peer receiving it would export
// it afresh rather than recognising it.
func WrapOutgoing(tag string, c capnp.Client) capnp.Client {
	return capnp.NewClient(&outgoing{inner: c, tag: tag})
}

// WrapIncoming returns a capability that behaves exactly like c while
// reporting any call delivered to it that has not returned within budget.
func WrapIncoming(tag string, c capnp.Client, budget time.Duration) capnp.Client {
	return capnp.NewClient(&incoming{inner: c, tag: tag, budget: budget})
}

type outgoing struct {
	inner capnp.Client
	tag   string
}

func (o *outgoing) Send(ctx context.Context, s capnp.Send) (*capnp.Answer, capnp.ReleaseFunc) {
	ans, rel := o.inner.SendCall(ctx, s)
	done := track(o.tag, callName(s.Method), hangdetect.BudgetForContext(ctx))
	// The channel is read here, before returning, rather than inside the
	// goroutine: the caller owns rel and may release the answer as soon as it
	// has its result, and reading through a released answer is not safe. The
	// channel value itself is an ordinary channel and stays valid afterwards.
	resolved := ans.Done()
	go func() {
		<-resolved
		done()
	}()
	return ans, rel
}

func (o *outgoing) Recv(ctx context.Context, r capnp.Recv) capnp.PipelineCaller {
	return o.inner.RecvCall(ctx, r)
}

// Brand is delegated rather than zeroed so the RPC layer can still recognise
// what it is holding — a wrapper that lies about its identity is the kind of
// thing that works until the day a capability is passed somewhere.
func (o *outgoing) Brand() capnp.Brand {
	s := o.inner.Snapshot()
	defer s.Release()
	return s.Brand()
}

func (o *outgoing) Shutdown()      { o.inner.Release() }
func (o *outgoing) String() string { return "rpcwatch.outgoing(" + o.inner.String() + ")" }

type incoming struct {
	inner  capnp.Client
	tag    string
	budget time.Duration
}

func (i *incoming) Send(ctx context.Context, s capnp.Send) (*capnp.Answer, capnp.ReleaseFunc) {
	return i.inner.SendCall(ctx, s)
}

func (i *incoming) Recv(ctx context.Context, r capnp.Recv) capnp.PipelineCaller {
	done := track(i.tag, callName(r.Method), i.budget)
	// Recv returns as soon as the call is queued, so the handler's own
	// duration is not observable from here. The returner is: every path out of
	// a handler — success, error, and Recv.Reject — ends at Return, and a
	// handler that never reaches one is precisely the stall being watched for.
	r.Returner = &trackedReturner{Returner: r.Returner, done: done}
	return i.inner.RecvCall(ctx, r)
}

func (i *incoming) Brand() capnp.Brand {
	s := i.inner.Snapshot()
	defer s.Release()
	return s.Brand()
}

func (i *incoming) Shutdown()      { i.inner.Release() }
func (i *incoming) String() string { return "rpcwatch.incoming(" + i.inner.String() + ")" }

// trackedReturner retires a tracked call when the method it belongs to
// returns. Everything else is the wrapped returner's, untouched.
type trackedReturner struct {
	capnp.Returner
	done func()
}

func (t *trackedReturner) Return() {
	t.Returner.Return()
	t.done()
}

// knownNames maps interface and method ids to the schema's own names, for the
// incoming direction.
//
// An outgoing call carries its names: the generated client code fills them in.
// An *incoming* one does not, because the names live in the generated server's
// method table and a ClientHook.Recv runs before that table is consulted — so
// without this a server-side report reads "@0xd281f133906f9f01.@2", which
// names the stall in the one vocabulary nobody debugging it is fluent in.
var (
	namesMu    sync.RWMutex
	knownNames = map[[2]uint64]string{}
)

// RegisterMethodNames teaches this package the names of an interface's
// methods, from the table the generated code already builds. Call it once per
// interface that will be wrapped for incoming calls; registering the same
// method twice is harmless.
func RegisterMethodNames(methods []server.Method) {
	namesMu.Lock()
	defer namesMu.Unlock()
	for _, sm := range methods {
		m := sm.Method
		if m.MethodName == "" {
			continue
		}
		knownNames[key(m)] = shortInterface(m.InterfaceName) + "." + m.MethodName
	}
}

func key(m capnp.Method) [2]uint64 { return [2]uint64{m.InterfaceID, uint64(m.MethodID)} }

// callName renders a method for a report. The schema's own names are used when
// the call carries them, then the registry above, and only then the numeric
// form — which remains reachable for a peer built from a schema this binary
// does not have, and is still better than nothing.
func callName(m capnp.Method) string {
	if m.MethodName == "" {
		namesMu.RLock()
		name, ok := knownNames[key(m)]
		namesMu.RUnlock()
		if ok {
			return "rpc " + name
		}
		return "rpc " + m.String()
	}
	if m.InterfaceName == "" {
		return "rpc " + m.MethodName
	}
	return "rpc " + shortInterface(m.InterfaceName) + "." + m.MethodName
}

// shortInterface drops the schema file and package qualification capnp puts in
// front of an interface name, leaving "EditorService" rather than
// "editor.capnp:EditorService".
func shortInterface(name string) string {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == ':' || name[i] == '.' {
			return name[i+1:]
		}
	}
	return name
}
