package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// sizedTestModel builds a client.Model with a non-zero terminal size (so
// View() renders real content instead of the zero-width "loading…"
// placeholder) and a zero-value RPC (its underlying capnp call fails
// immediately, without needing a real server — see resync_test.go).
func sizedTestModel(bufID uint32, absPath string) client.Model {
	m := client.New(&client.RPC{}, bufID, "hello\n", 0, absPath, "/tmp", nil, false, 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return updated.(client.Model)
}

// findRoutableMsg runs cmd (and recursively unwraps any tea.BatchMsg it
// produces — key handlers commonly bundle their RPC command together with
// unrelated ones like a highlight refresh) until it finds a value
// implementing client.RoutableMsg, or returns nil if none is found.
func findRoutableMsg(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			if found := findRoutableMsg(sub); found != nil {
				return found
			}
		}
		return nil
	}
	if _, ok := msg.(client.RoutableMsg); ok {
		return msg
	}
	return nil
}

// TestRoutableMsgReachesInactiveBuffer is a regression test for App's
// generic dispatch fallback (Update, just before the main switch), which
// used to route every message unconditionally to a.buffers[a.active]. A
// bufID-carrying RoutableMsg (saveFailedMsg here, but the same mechanism
// covers savedMsg/savedAsMsg/discardRecoveryMsg/discardRecoveryFailedMsg/
// applyOpFailedMsg) for a buffer the user has since switched away from was
// silently absorbed by that buffer's own bufID guard and dropped — never
// applied to the buffer it was actually about — since it was only ever
// handed to the active buffer. App.Update must instead look up the buffer
// by RouteBufID() and deliver the message there directly.
func TestRoutableMsgReachesInactiveBuffer(t *testing.T) {
	active := sizedTestModel(1, "/tmp/a.go")
	inactive := sizedTestModel(2, "/tmp/b.go")
	a := App{
		buffers: []client.Model{active, inactive},
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}

	// Trigger buffer 2's real Save flow (ctrl+s). Its rpc is a zero-value
	// RPC{}, so the underlying capnp call fails immediately — this produces
	// a genuine saveFailedMsg{bufID: 2, ...} via the same production code
	// path (doSave -> doSaveNow) a real failed save takes.
	_, cmd := inactive.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a non-nil command from ctrl+s (doSaveNow)")
	}
	msg := findRoutableMsg(cmd)
	if msg == nil {
		t.Fatal("expected a client.RoutableMsg from the failed save, found none")
	}

	updated, _ := a.Update(msg)
	a2 := updated.(App)

	if a2.active != 0 {
		t.Errorf("active = %d, want unchanged 0 (routing must not switch tabs)", a2.active)
	}
	if len(a2.buffers) != 2 {
		t.Fatalf("len(buffers) = %d, want unchanged 2", len(a2.buffers))
	}

	view0 := a2.buffers[0].View().Content
	view1 := a2.buffers[1].View().Content
	if strings.Contains(view0, "ERR:") {
		t.Errorf("active buffer's (bufID 1) view shows the save error — it should have landed on bufID 2 instead:\n%s", view0)
	}
	if !strings.Contains(view1, "ERR:") {
		t.Errorf("inactive buffer's (bufID 2) view does not show the save error — routing failed to reach it:\n%s", view1)
	}
}

// TestRoutableMsgDroppedWhenBufferGone covers the buffer having closed
// entirely by the time the message arrives: routing must not panic and must
// leave the (unrelated) remaining buffer untouched.
func TestRoutableMsgDroppedWhenBufferGone(t *testing.T) {
	only := sizedTestModel(1, "/tmp/a.go")
	a := App{
		buffers: []client.Model{only},
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}

	// bufID 99 doesn't exist in a.buffers.
	closed := sizedTestModel(99, "/tmp/gone.go")
	_, cmd := closed.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("expected a non-nil command from ctrl+s")
	}
	msg := findRoutableMsg(cmd)
	if msg == nil {
		t.Fatal("expected a client.RoutableMsg from the failed save, found none")
	}

	updated, gotCmd := a.Update(msg)
	a2 := updated.(App)

	if gotCmd != nil {
		t.Error("expected a nil command when the routed bufID has no matching buffer")
	}
	if len(a2.buffers) != 1 || a2.buffers[0].BufID() != 1 {
		t.Errorf("buffers mutated on an unmatched bufID: %+v", a2.buffers)
	}
	if strings.Contains(a2.buffers[0].View().Content, "ERR:") {
		t.Error("unrelated buffer should not show an error for a message about a closed buffer")
	}
}
