package plugin

import (
	"context"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/sdk/pluginproto"
)

// recordingEditHandler records the FromLine of each linesChanged it receives,
// pausing a little at random first so calls that overlap get the chance to
// finish out of order.
type recordingEditHandler struct {
	mu    sync.Mutex
	lines []uint32
	all   chan struct{}
	n     int
}

func (h *recordingEditHandler) LinesChanged(_ context.Context, call pluginproto.EditEventHandler_linesChanged) error {
	time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lines = append(h.lines, call.Args().AtLine())
	if len(h.lines) == h.n {
		close(h.all)
	}
	return nil
}

// TestEditEventsReachAPluginInOrder is the regression test for edit events sent
// on a goroutine each: dispatched back to back, they reached the plugin in
// whatever order the scheduler chose, so a plugin tracking positions applied
// two edits reversed. The range fields make this matter more, since which lines
// an edit keeps depends on the edits before it.
func TestEditEventsReachAPluginInOrder(t *testing.T) {
	const n = 200
	h := &recordingEditHandler{all: make(chan struct{}), n: n}
	client := pluginproto.EditEventHandler_ServerToClient(h)
	defer client.Release()
	m := &Manager{plugins: []*registeredPlugin{{editHandler: client}}}

	for i := range n {
		m.DispatchEditEvent(context.Background(), 1, "/a.txt", EditRange{FromLine: uint32(i), ToLine: uint32(i) + 1, LineDelta: -1})
	}
	select {
	case <-h.all:
	case <-time.After(20 * time.Second):
		t.Fatalf("only %d of %d events arrived", len(h.lines), n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, line := range h.lines {
		if line != uint32(i) {
			t.Fatalf("event %d arrived in position %d", line, i)
		}
	}
}
