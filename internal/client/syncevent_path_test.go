package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/syncevent"
)

// TestResyncEventsCarryAPathEvenWhenTheRPCFails is why the path is captured
// before the round trip rather than taken from its result.
//
// GetBufferSnapshot returns an empty path on failure, so the failure event —
// the one most worth finding — was the only one a path-filtered query could not
// see. Any resync_failed reported for the whole workspace and none for the file
// it happened in.
func TestResyncEventsCarryAPathEvenWhenTheRPCFails(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())

	m := newTestModel("hello\n")
	m.rpc = &RPC{} // no connection: GetBufferSnapshot fails
	m.bufID = 3
	m.filePath = "/w/target.go"

	cmd := m.resyncFromServer("test cause")
	if cmd == nil {
		t.Fatal("resyncFromServer returned no command")
	}
	cmd() // run it so the failure is recorded

	for _, kind := range []syncevent.Kind{syncevent.ResyncStarted, syncevent.ResyncFailed} {
		events, err := syncevent.Read(syncevent.ReadOptions{Kind: kind, Path: "target.go"})
		if err != nil {
			t.Fatalf("syncevent.Read(%s): %v", kind, err)
		}
		if len(events) == 0 {
			t.Errorf("no %s event is findable by path — a path-filtered query "+
				"misses it entirely", kind)
			continue
		}
		if got := events[len(events)-1].Path; got != "/w/target.go" {
			t.Errorf("%s recorded path %q, want /w/target.go", kind, got)
		}
	}
}
