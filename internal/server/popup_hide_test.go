package server

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// popupCallback is a partial ClientCallback recording the popup pushes it
// receives. block, when non-nil, is waited on before answering a hide, standing
// in for a wedged window; it gives up when the call's context expires, as the
// real handler would.
type popupCallback struct {
	proto.ClientCallback_Server
	mu    sync.Mutex
	got   []string
	block chan struct{}
}

func (f *popupCallback) record(s string) {
	f.mu.Lock()
	f.got = append(f.got, s)
	f.mu.Unlock()
}

func (f *popupCallback) pushes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

func (f *popupCallback) ShowPluginPopup(_ context.Context, call proto.ClientCallback_showPluginPopup) error {
	title, _ := call.Args().Title()
	f.record("show " + title)
	return nil
}

func (f *popupCallback) HidePluginPopup(ctx context.Context, _ proto.ClientCallback_hidePluginPopup) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
	f.record("hide")
	return nil
}

func (f *popupCallback) ShowInputPrompt(_ context.Context, call proto.ClientCallback_showInputPrompt) error {
	title, _ := call.Args().Title()
	f.record("prompt " + title)
	return nil
}

func (f *popupCallback) HideInputPrompt(ctx context.Context, _ proto.ClientCallback_hideInputPrompt) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
	f.record("unprompt")
	return nil
}

func popupService(cbs ...proto.ClientCallback) *editorService {
	s := &editorService{clientMap: map[uint64]*clientEntry{}}
	for i, cb := range cbs {
		s.clientMap[uint64(i+1)] = &clientEntry{callback: cb}
	}
	return s
}

// TestClosingAPopupDoesNotWaitForAWedgedWindow is a regression test. The four
// handlers that close a popup or prompt told every window to hide it one at a
// time, with no deadline and without releasing the call, so one wedged window
// held the call of the window that answered the popup, and every call queued
// behind it on that connection.
func TestClosingAPopupDoesNotWaitForAWedgedWindow(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	wedgedFake := &popupCallback{block: release}
	wedged := proto.ClientCallback_ServerToClient(wedgedFake)
	defer wedged.Release()
	fine := &popupCallback{}
	responsive := proto.ClientCallback_ServerToClient(fine)
	defer responsive.Release()

	s := popupService(wedged, responsive)
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	calls := []struct {
		name string
		call func(context.Context) error
		want string
	}{
		{"PluginPopupSelected", func(ctx context.Context) error {
			fut, rel := cl.PluginPopupSelected(ctx, func(p proto.EditorService_pluginPopupSelected_Params) error {
				p.SetIndex(0)
				return nil
			})
			defer rel()
			_, err := fut.Struct()
			return err
		}, "hide"},
		{"PluginPopupCancelled", func(ctx context.Context) error {
			fut, rel := cl.PluginPopupCancelled(ctx, nil)
			defer rel()
			_, err := fut.Struct()
			return err
		}, "hide"},
		{"PluginInputConfirmed", func(ctx context.Context) error {
			fut, rel := cl.PluginInputConfirmed(ctx, func(p proto.EditorService_pluginInputConfirmed_Params) error {
				return p.SetText("x")
			})
			defer rel()
			_, err := fut.Struct()
			return err
		}, "unprompt"},
		{"PluginInputCancelled", func(ctx context.Context) error {
			fut, rel := cl.PluginInputCancelled(ctx, nil)
			defer rel()
			_, err := fut.Struct()
			return err
		}, "unprompt"},
	}
	for i, c := range calls {
		// The caller's own deadline is no bound here: an in-process future
		// whose callee never returns is not resolved by cancelling it.
		done := make(chan error, 1)
		go func() { done <- c.call(context.Background()) }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("%s did not return; it must not wait for a wedged window", c.name)
		}
		// The responsive window still has to be told, or returning fast by
		// telling nobody would pass.
		deadline := time.Now().Add(3 * time.Second)
		for len(fine.pushes()) <= i {
			if time.Now().After(deadline) {
				t.Fatalf("%s: the responsive window was never told to %s", c.name, c.want)
			}
			time.Sleep(time.Millisecond)
		}
		if got := fine.pushes()[i]; got != c.want {
			t.Errorf("%s: responsive window got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestPopupPushesReachAWindowInOrder is a regression test. Showing a popup
// pushed to the windows from a goroutine that then took popupDispatchMu, so
// two shows could take the lock in either order, and hiding pushed
// synchronously, so a hide could overtake the show it answered. A window then
// showed a popup that had already been closed, bound to no callback, or the
// older of two popups. Every push now goes through one ordered queue, in the
// order the server's popup state changed.
func TestPopupPushesReachAWindowInOrder(t *testing.T) {
	fake := &popupCallback{}
	cb := proto.ClientCallback_ServerToClient(fake)
	defer cb.Release()
	s := popupService(cb)
	cl := proto.EditorService_ServerToClient(&connSvc{editorService: s, connID: 1})
	defer cl.Release()

	const rounds = 100
	var want []string
	for i := range rounds {
		title := fmt.Sprint(i)
		if i%2 == 0 {
			s.PluginShowPopup(title, nil, func(string) {}, func() {})
			fut, rel := cl.PluginPopupCancelled(context.Background(), nil)
			if _, err := fut.Struct(); err != nil {
				t.Fatal(err)
			}
			rel()
			want = append(want, "show "+title, "hide")
		} else {
			s.PluginShowInputPrompt(title, "", func(string) {}, func() {})
			fut, rel := cl.PluginInputCancelled(context.Background(), nil)
			if _, err := fut.Struct(); err != nil {
				t.Fatal(err)
			}
			rel()
			want = append(want, "prompt "+title, "unprompt")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.pushes()) < len(want) {
		if time.Now().After(deadline) {
			t.Fatalf("got %d pushes, want %d", len(fake.pushes()), len(want))
		}
		time.Sleep(time.Millisecond)
	}
	got := fake.pushes()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("push %d = %q, want %q (first %d: %q)", i, got[i], want[i], i+1, got[:i+1])
		}
	}
}
