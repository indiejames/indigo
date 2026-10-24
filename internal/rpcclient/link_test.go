package rpcclient

import (
	"testing"

	proto "github.com/indiejames/indigo/internal/proto"
)

// rpcOver returns an RPC whose only connection is svc, for a test that calls
// methods against a fake service.
func rpcOver(svc proto.EditorService) *RPC {
	r := &RPC{cb: &callbackServer{}, insertHookChars: make(map[string]bool)}
	r.link.Store(&link{svc: svc, maxLocal: &r.maxLocal})
	return r
}

// TestFirstLinkPassesBufferIDsThrough: before any reconnect the editor's ids
// are the server's.
func TestFirstLinkPassesBufferIDsThrough(t *testing.T) {
	r := rpcOver(proto.EditorService{})
	l := r.cur()
	if got := l.fromServer(7); got != 7 {
		t.Errorf("fromServer(7) = %d, want 7", got)
	}
	if got := l.toServer(7); got != 7 {
		t.Errorf("toServer(7) = %d, want 7", got)
	}
}

// TestReconnectedLinkTranslatesBufferIDs pins the three rules a reconnected
// link keeps: a reattached buffer keeps the editor's id, a buffer first seen
// after the reconnect gets an id no earlier buffer had, and an editor id with
// no buffer on the new server names none there rather than whichever buffer
// happens to share its number.
func TestReconnectedLinkTranslatesBufferIDs(t *testing.T) {
	r := rpcOver(proto.EditorService{})
	first := r.cur()
	first.fromServer(1) // the editor opened buffers 1..3 on the first server
	first.fromServer(2)
	first.fromServer(3)

	l := &link{mapped: true, toRemote: map[uint32]uint32{}, toLocal: map[uint32]uint32{}, maxLocal: &r.maxLocal}
	r.link.Store(l)

	// Buffer 3 reattached; the new server calls it 1.
	l.bind(3, 1)
	if got := l.toServer(3); got != 1 {
		t.Errorf("toServer(3) = %d, want 1 (the new server's id for it)", got)
	}
	if got := l.fromServer(1); got != 3 {
		t.Errorf("fromServer(1) = %d, want 3 (the editor's id for it)", got)
	}

	// Buffer 1 has not been reattached yet. The new server's buffer 1 is a
	// different file, so passing the id through would poll the wrong buffer.
	if got := l.toServer(1); got != 0 {
		t.Errorf("toServer(1) = %d before buffer 1 is reattached, want 0 (no buffer)", got)
	}

	// A file opened after the reconnect: the new server calls it 2, and the
	// editor already has a buffer 2.
	if got := l.fromServer(2); got != 4 {
		t.Errorf("fromServer(2) = %d for a newly opened buffer, want 4 (past every id the editor has)", got)
	}
	if got := l.toServer(4); got != 2 {
		t.Errorf("toServer(4) = %d, want 2", got)
	}

	if l.toServer(0) != 0 || l.fromServer(0) != 0 {
		t.Error("0 means no buffer and must stay 0 both ways")
	}
}
