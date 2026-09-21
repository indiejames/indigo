package server

import (
	"strings"
	"testing"
)

// TestServedConnectionsGoThroughTheHangWatcher is the dispatch half of the
// pair rpcwatch's own tests make up. Those prove the wrapper tracks and
// retires a call; none of them would notice serve() handing capnp the bare
// capability instead, because a bare capability behaves identically in every
// way except the one that matters here. Verified by dropping the
// rpcwatch.WrapIncoming call, which fails only this test.
func TestServedConnectionsGoThroughTheHangWatcher(t *testing.T) {
	opts := bootstrapOptions(&connSvc{})
	got := opts.BootstrapClient.String()
	if !strings.Contains(got, "rpcwatch.incoming") {
		t.Errorf("bootstrap capability = %q; incoming calls are not being watched", got)
	}
	if opts.Logger == nil {
		t.Error("the connection logger was lost")
	}
}
