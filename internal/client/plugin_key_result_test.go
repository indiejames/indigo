package client

import (
	"testing"

	"github.com/indiejames/indigo/internal/document"
)

// TestPluginKeyResultMsgDiscardsStaleBuffer is a regression test:
// pluginKeyResultMsg had no bufID, unlike inlayHintsMsg/semanticTokensMsg,
// so a plugin key RPC response that arrives after the user has switched
// buffers (e.g. a slow OnInsert hook, or a normal-mode plugin key binding
// racing a tab switch) would move the cursor and set capture mode in
// whatever buffer is now active instead of being discarded.
func TestPluginKeyResultMsgDiscardsStaleBuffer(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.bufID = 2
	m.cursor = document.Pos{Line: 0, Col: 0}

	msg := pluginKeyResultMsg{
		bufID: 1, // the buffer the RPC was made against — no longer active
		result: PluginKeyResult{
			HasCursor:   true,
			CursorLine:  1,
			CursorCol:   3,
			CaptureKeys: 2,
		},
	}

	updated, cmd := m.Update(msg)
	m2 := updated.(Model)

	if m2.cursor.Line != 0 || m2.cursor.Col != 0 {
		t.Errorf("cursor = %+v, want unchanged (0,0) — stale-buffer result must be discarded", m2.cursor)
	}
	if m2.captureMode {
		t.Error("captureMode should not be set from a stale-buffer result")
	}
	if cmd != nil {
		t.Error("expected no command for a discarded stale-buffer result")
	}
}

// TestPluginKeyResultMsgAppliesForCurrentBuffer is the happy-path
// counterpart: a result matching the model's current bufID must still
// apply its cursor move and capture-mode update as before.
func TestPluginKeyResultMsgAppliesForCurrentBuffer(t *testing.T) {
	m := newTestModel("hello\nworld\n")
	m.bufID = 1
	m.cursor = document.Pos{Line: 0, Col: 0}

	msg := pluginKeyResultMsg{
		bufID: 1,
		result: PluginKeyResult{
			HasCursor:   true,
			CursorLine:  1,
			CursorCol:   3,
			CaptureKeys: 2,
		},
	}

	updated, _ := m.Update(msg)
	m2 := updated.(Model)

	if m2.cursor.Line != 1 || m2.cursor.Col != 3 {
		t.Errorf("cursor = %+v, want (1,3)", m2.cursor)
	}
	if !m2.captureMode || m2.captureRemaining != 2 {
		t.Errorf("captureMode=%v captureRemaining=%d, want true/2", m2.captureMode, m2.captureRemaining)
	}
}
