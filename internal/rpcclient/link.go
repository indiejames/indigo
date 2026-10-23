package rpcclient

import (
	"sync"
	"sync/atomic"

	"capnproto.org/go/capnp/v3/rpc"

	proto "github.com/indiejames/indigo/internal/proto"
)

// link is one connection to a server: the capnp connection, the service on it,
// and the client id the server gave this window when it connected.
//
// An RPC holds its current link behind an atomic pointer, and every call reads
// it afresh, so a reconnect (see reconnect.go) can replace the connection
// underneath the whole editor without any caller noticing. The previous link is
// dead by then, so a call that picked it up just before the swap fails like any
// other call to a server that has gone.
type link struct {
	conn        *rpc.Conn
	svc         proto.EditorService
	clientID    uint64
	serverStale bool

	// Buffer ids are the server's, and a server numbers its buffers from 1. A
	// window that reconnects usually meets a new server (the old one exits when
	// its last client goes), whose ids mean different buffers. The editor's own
	// ids are therefore kept, and a reconnected link translates between the two.
	//
	// The first link needs none of that: there the editor's ids *are* the
	// server's, and mapped is false so that the common case costs nothing.
	mapped   bool
	mu       sync.Mutex
	toRemote map[uint32]uint32
	toLocal  map[uint32]uint32
	// maxLocal is the RPC's highest editor-side id, shared by every link, so a
	// buffer first seen on a reconnected link gets an id no buffer opened
	// before the reconnect already has.
	maxLocal *atomic.Uint32
}

// toServer translates an editor-side buffer id into this link's server's id.
//
// On a reconnected link an id with no counterpart becomes 0, which no server
// buffer has, so the call fails. Passing it through instead could name an
// unrelated buffer that happens to carry the same number on the new server —
// a poll would then deliver another file's edits.
func (l *link) toServer(local uint32) uint32 {
	if local == 0 {
		return 0
	}
	if !l.mapped {
		l.noteLocal(local)
		return local
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.toRemote[local]
}

// fromServer translates this link's server's buffer id into the editor's,
// giving a buffer seen for the first time a new editor-side id.
func (l *link) fromServer(remote uint32) uint32 {
	if remote == 0 {
		return 0
	}
	if !l.mapped {
		l.noteLocal(remote)
		return remote
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if local, ok := l.toLocal[remote]; ok {
		return local
	}
	local := l.maxLocal.Add(1)
	l.toRemote[local] = remote
	l.toLocal[remote] = local
	return local
}

// bind records that the editor's buffer local is this server's remote, for a
// buffer the editor already had open before reconnecting (RPC.Reattach).
func (l *link) bind(local, remote uint32) {
	if !l.mapped {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.toRemote[local] = remote
	l.toLocal[remote] = local
}

// noteLocal keeps maxLocal at or above every id the editor has been handed.
func (l *link) noteLocal(id uint32) {
	for {
		cur := l.maxLocal.Load()
		if id <= cur || l.maxLocal.CompareAndSwap(cur, id) {
			return
		}
	}
}

// cur returns the link calls should go out on now.
//
// An RPC that never connected (the zero value, which tests build freely) gets
// an empty link: its service is the null capability, so a call fails with an
// error instead of a nil dereference, as it did when these were the RPC's own
// zero-valued fields.
func (r *RPC) cur() *link {
	if l := r.link.Load(); l != nil {
		return l
	}
	return &link{maxLocal: &r.maxLocal}
}
