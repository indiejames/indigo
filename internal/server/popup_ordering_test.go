package server

import (
	"context"
	"testing"
	"time"

	proto "github.com/indiejames/indigo/internal/proto"
)

// fakeClientCallback is a partial proto.ClientCallback_Server: only
// ShowPluginPopup is implemented (every other method would nil-pointer
// panic if called, which none of these tests do).
type fakeClientCallback struct {
	proto.ClientCallback_Server
	onShowPopup func(title string)
}

func (f *fakeClientCallback) ShowPluginPopup(_ context.Context, call proto.ClientCallback_showPluginPopup) error {
	title, _ := call.Args().Title()
	f.onShowPopup(title)
	_, err := call.AllocResults()
	return err
}

// TestPluginShowPopupWaitsForDispatchMutex is a regression test: per-client
// dispatch goroutines fired independently per PluginShowPopup call had no
// ordering guarantee relative to *other* calls, so a second (newer)
// popup's UI push could race an earlier one's and reach a client first —
// leaving the client showing stale popup contents bound to an
// already-replaced server-side callback (see popupDispatchMu's doc
// comment). This holds popupDispatchMu from the test goroutine, standing
// in for another call's in-progress dispatch, and confirms
// PluginShowPopup's own client-notification goroutine actually blocks on
// it rather than proceeding immediately.
func TestPluginShowPopupWaitsForDispatchMutex(t *testing.T) {
	called := make(chan struct{}, 1)
	fake := &fakeClientCallback{onShowPopup: func(string) { called <- struct{}{} }}
	cb := proto.ClientCallback_ServerToClient(fake)
	defer cb.Release()

	s := &editorService{
		clientMap: map[uint64]*clientEntry{1: {callback: cb}},
	}

	// Simulate another PluginShowPopup/PluginShowInputPrompt call's
	// dispatch already in progress.
	s.popupDispatchMu.Lock()

	s.PluginShowPopup("A", nil, func(string) {}, func() {})

	select {
	case <-called:
		s.popupDispatchMu.Unlock()
		t.Fatal("PluginShowPopup's client dispatch proceeded without waiting for popupDispatchMu")
	case <-time.After(100 * time.Millisecond):
	}

	s.popupDispatchMu.Unlock()

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("PluginShowPopup's client dispatch never proceeded after popupDispatchMu was released")
	}
}
