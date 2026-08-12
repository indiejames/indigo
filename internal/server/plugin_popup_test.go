package server

import (
	"testing"
	"time"
)

// TestPluginShowPopupCancelsPendingPopupOnPreemption is a regression test:
// PluginShowPopup used to unconditionally overwrite s.popupOnSelect/
// popupOnCancel, so a popup that was shown but never resolved (no select,
// no cancel) had its handler capability silently leaked when a second
// popup preempted it — onCancel (which releases the capability, see
// api.go's ShowPopup) was never invoked for the first popup.
func TestPluginShowPopupCancelsPendingPopupOnPreemption(t *testing.T) {
	s := &editorService{}

	firstCancelled := make(chan struct{})
	s.PluginShowPopup("first", nil, func(string) {}, func() { close(firstCancelled) })

	secondCancelled := make(chan struct{})
	s.PluginShowPopup("second", nil, func(string) {}, func() { close(secondCancelled) })

	select {
	case <-firstCancelled:
	case <-time.After(time.Second):
		t.Fatal("first popup's onCancel was never invoked when preempted by a second popup")
	}

	select {
	case <-secondCancelled:
		t.Fatal("second popup's onCancel was invoked without ever being preempted or resolved")
	default:
	}

	s.mu.Lock()
	hasPending := s.popupOnCancel != nil
	s.mu.Unlock()
	if !hasPending {
		t.Error("the second popup should still be the pending one after preempting the first")
	}
}

// TestPluginShowPopupNoPriorPopupDoesNotPanic covers the common case: the
// very first popup shown has no predecessor to cancel.
func TestPluginShowPopupNoPriorPopupDoesNotPanic(t *testing.T) {
	s := &editorService{}
	s.PluginShowPopup("only", nil, func(string) {}, func() {})
}

// TestPluginShowInputPromptCancelsPendingPromptOnPreemption is
// PluginShowPopup's counterpart for the input-prompt path.
func TestPluginShowInputPromptCancelsPendingPromptOnPreemption(t *testing.T) {
	s := &editorService{}

	firstCancelled := make(chan struct{})
	s.PluginShowInputPrompt("first", "", func(string) {}, func() { close(firstCancelled) })

	s.PluginShowInputPrompt("second", "", func(string) {}, func() {})

	select {
	case <-firstCancelled:
	case <-time.After(time.Second):
		t.Fatal("first prompt's onCancel was never invoked when preempted by a second prompt")
	}
}
