package rpcclient

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	proto "github.com/indiejames/indigo/internal/proto"
)

// callReportBufferState drives the real callback through capnp, as the server
// does, and returns how long it took.
func callReportBufferState(t *testing.T, ctx context.Context, cb *callbackServer) (proto.ClientCallback_reportBufferState_Results, time.Duration, error) {
	t.Helper()
	c := proto.ClientCallback_ServerToClient(cb)
	t.Cleanup(c.Release)
	start := time.Now()
	fut, rel := c.ReportBufferState(ctx, func(p proto.ClientCallback_reportBufferState_Params) error {
		p.SetBufId(7)
		return nil
	})
	t.Cleanup(rel)
	res, err := fut.Struct()
	return res, time.Since(start), err
}

// A connection with no dispatcher holds no buffer content and must say so,
// immediately.
//
// agenttools dials through DialStream exactly as an editor window does, but has
// no Bubble Tea loop to ask. This used to fall through to the 2s wait and then
// return an error, which check_buffer_consistency renders as NO ANSWER — the
// verdict reserved for a wedged window. Every consistency check run from an
// agent therefore accused the agent's own connection of being wedged, and a
// real bug report was misdiagnosed for two sessions on the strength of it.
func TestReportBufferStateAnswersNotKnownWithNoDispatcher(t *testing.T) {
	cb := &callbackServer{}
	res, took, err := callReportBufferState(t, context.Background(), cb)
	if err != nil {
		t.Fatalf("ReportBufferState: %v — a client with no dispatcher must answer, not fail", err)
	}
	if res.Known() {
		t.Error("known = true; a client with no dispatcher holds no buffer content")
	}
	if took > reportBufferStateTimeout/2 {
		t.Errorf("took %v; must answer at once rather than waiting out %v", took, reportBufferStateTimeout)
	}
}

// The wedged-window case must still be reported: a dispatcher that is
// installed but never answers is the finding NO ANSWER exists for, and the fix
// above must not have swallowed it.
func TestReportBufferStateStillFailsForAWedgedDispatcher(t *testing.T) {
	cb := &callbackServer{}
	cb.setSend(func(tea.Msg) {}) // installed, and never replies

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, _, err := callReportBufferState(t, ctx, cb); err == nil {
		t.Error("ReportBufferState returned nil for a dispatcher that never answered; NO ANSWER must survive")
	}
}

// The ordinary path still passes the App's answer through.
func TestReportBufferStateReturnsTheAppsAnswer(t *testing.T) {
	cb := &callbackServer{}
	cb.setSend(func(msg tea.Msg) {
		m, ok := msg.(ReportBufferStateMsg)
		if !ok {
			return
		}
		m.Reply <- BufferStateReport{
			Known: true, Version: 42, Generation: 3, Dirty: true,
			ContentSha256: []byte{0xab, 0xcd},
		}
	})

	res, _, err := callReportBufferState(t, context.Background(), cb)
	if err != nil {
		t.Fatalf("ReportBufferState: %v", err)
	}
	sha, _ := res.ContentSha256()
	if !res.Known() || res.Version() != 42 || res.Generation() != 3 || !res.Dirty() || len(sha) != 2 {
		t.Errorf("got known=%v version=%d generation=%d dirty=%v sha=%x, want the App's answer",
			res.Known(), res.Version(), res.Generation(), res.Dirty(), sha)
	}
}
