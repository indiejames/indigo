package client

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runKeys feeds each key through the full Update dispatch, in order — the
// same path a live keystroke takes, and the same path macro recording taps
// into (Update's tea.KeyMsg case in model.go).
func runKeys(t *testing.T, m Model, keys ...tea.KeyMsg) Model {
	t.Helper()
	for _, k := range keys {
		newModel, _ := m.Update(k)
		got, ok := newModel.(Model)
		if !ok {
			t.Fatalf("Update(%v) returned non-Model %T", k, newModel)
		}
		m = got
	}
	return m
}

// TestMacroRecordExcludesToggleKeys is a regression test for the core
// recording contract: the q keypresses that start and stop recording must
// never themselves end up in the recorded sequence, only the keys typed in
// between.
func TestMacroRecordExcludesToggleKeys(t *testing.T) {
	m := newTestModel("one\ntwo\nthree\n")
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial

	m = runKeys(t, m,
		fakeKey("q"),   // start recording (excluded)
		fakeKey("A"),   // append at line end (recorded)
		fakeKey("!"),   // insert '!' (recorded)
		fakeKey("esc"), // back to normal (recorded)
		fakeKey("q"),   // stop recording (excluded)
	)

	if m.macroRecording {
		t.Fatal("macroRecording still true after second q")
	}
	if len(m.macroKeys) != 0 {
		t.Errorf("macroKeys = %v, want cleared after stopping", m.macroKeys)
	}
	want := []tea.KeyMsg{fakeKey("A"), fakeKey("!"), fakeKey("esc")}
	if len(m.lastMacro) != len(want) {
		t.Fatalf("lastMacro = %v, want %v", m.lastMacro, want)
	}
	for i, k := range want {
		if m.lastMacro[i].String() != k.String() {
			t.Errorf("lastMacro[%d] = %q, want %q", i, m.lastMacro[i].String(), k.String())
		}
	}
	// The recorded edit itself must also have actually happened once, live.
	if got := m.buf.Content(); got != "one!\ntwo\nthree\n" {
		t.Errorf("buffer after recording = %q, want %q", got, "one!\ntwo\nthree\n")
	}
}

// TestMacroReplayReproducesRecordedEdit verifies that feeding the recorded
// key sequence back through Update — what executeMacroReplay's tea.Sequence
// does at runtime, one key delivered to Update at a time in order —
// reproduces the same edit on a different line. tea.Sequence's own delivery
// ordering is bubbletea's guarantee, not re-tested here; what's under test is
// that lastMacro holds the right keys and that replaying them through the
// normal dispatch path does the right thing.
func TestMacroReplayReproducesRecordedEdit(t *testing.T) {
	m := newTestModel("one\ntwo\nthree\n")
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial

	m = runKeys(t, m, fakeKey("q"), fakeKey("A"), fakeKey("!"), fakeKey("esc"), fakeKey("q"))
	if got := m.buf.Content(); got != "one!\ntwo\nthree\n" {
		t.Fatalf("setup: buffer = %q, want %q", got, "one!\ntwo\nthree\n")
	}

	// Move to line 1 ("two") and replay the recorded macro there.
	m = runKeys(t, m, fakeKey("down"))
	if m.cursor.Line != 1 {
		t.Fatalf("setup: cursor.Line = %d, want 1", m.cursor.Line)
	}
	m = runKeys(t, m, m.lastMacro...)

	want := "one!\ntwo!\nthree\n"
	if got := m.buf.Content(); got != want {
		t.Errorf("buffer after replay = %q, want %q", got, want)
	}
}

// TestMacroReplayBlockedWhileRecording verifies @ refuses to replay (and
// does nothing) while a recording is still in progress, rather than
// replaying into itself.
func TestMacroReplayBlockedWhileRecording(t *testing.T) {
	m := newTestModel("one\n")
	m.macroRecording = true
	m.lastMacro = []tea.KeyMsg{fakeKey("x")} // a previous macro exists

	newModel, cmd := executeMacroReplay(m)
	got := newModel.(Model)
	if cmd != nil {
		t.Error("executeMacroReplay while recording returned a non-nil cmd, want no replay")
	}
	if got.status == "" {
		t.Error("executeMacroReplay while recording should set a status message")
	}
}

// TestMacroReplayNoopWhenEmpty verifies @ with nothing recorded yet is a
// harmless no-op with a status message, not a panic or a replay of a stale
// zero-value macro.
func TestMacroReplayNoopWhenEmpty(t *testing.T) {
	m := newTestModel("one\n")

	newModel, cmd := executeMacroReplay(m)
	got := newModel.(Model)
	if cmd != nil {
		t.Error("executeMacroReplay with no recorded macro returned a non-nil cmd, want no replay")
	}
	if got.status == "" {
		t.Error("executeMacroReplay with no recorded macro should set a status message")
	}
}

// TestMacroRecordLiteralQInInsertMode is a regression test for the obvious
// question the q-toggles-recording design raises: how do you get a literal
// 'q' character into a macro? Answer: q only toggles recording when it's a
// Normal-mode *command* — in Insert mode it's an ordinary self-inserted
// character, handled entirely outside the Normal-mode dispatch table q's
// toggle is registered in, so it types 'q' and gets recorded like any other
// key instead of stopping the recording.
func TestMacroRecordLiteralQInInsertMode(t *testing.T) {
	m := newTestModel("one\n")
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial

	m = runKeys(t, m,
		fakeKey("q"),   // start recording
		fakeKey("i"),   // enter insert mode
		fakeKey("q"),   // literal 'q' — must NOT stop recording
		fakeKey("u"),   // literal 'u'
		fakeKey("esc"), // back to normal
		fakeKey("q"),   // stop recording
	)

	if m.macroRecording {
		t.Fatal("recording should have stopped by the final q")
	}
	if got := m.buf.Content(); got != "quone\n" {
		t.Fatalf("buffer = %q, want %q (literal q typed, not treated as stop-toggle)", got, "quone\n")
	}
	want := []string{"i", "q", "u", "esc"}
	if len(m.lastMacro) != len(want) {
		t.Fatalf("lastMacro = %v, want keys %v", m.lastMacro, want)
	}
	for i, k := range want {
		if got := m.lastMacro[i].String(); got != k {
			t.Errorf("lastMacro[%d] = %q, want %q", i, got, k)
		}
	}
}

// TestMacroExcludesRejectedReplayFromRecording is a regression test for a
// self-referential-replay bug: pressing @ in Normal mode while a recording
// is in progress is rejected by executeMacroReplay (no nested replay runs
// at that moment) — but without macroReplayBlocked's special handling, that
// rejected @ keypress would still get *recorded* into the macro like any
// other key. Later, replaying the finished macro would redeliver that
// recorded @ — no longer "while recording" at that point, so it would
// actually run — scheduling another full replay of the same macro, which
// itself contains the same @, recursing without end. Excluding the rejected
// @ from macroKeys prevents this at the root: @ never appears in lastMacro
// at all, so a replay can never re-trigger itself.
func TestMacroExcludesRejectedReplayFromRecording(t *testing.T) {
	m := newTestModel("one\ntwo\n")
	m.rpc = &RPC{} // zero-value RPC is safe: ClientID() just reads a field, no dial

	m = runKeys(t, m,
		fakeKey("q"),   // start recording
		fakeKey("i"),   // enter insert mode
		fakeKey("x"),   // literal 'x'
		fakeKey("esc"), // back to normal
		fakeKey("@"),   // replay attempt while recording — rejected, must NOT be recorded
		fakeKey("q"),   // stop recording
	)

	if m.macroRecording {
		t.Fatal("recording should have stopped by the final q")
	}
	if m.macroReplayBlocked {
		t.Error("macroReplayBlocked left set — should be cleared the moment it's consumed")
	}
	want := []string{"i", "x", "esc"}
	if len(m.lastMacro) != len(want) {
		t.Fatalf("lastMacro = %v, want keys %v (no @)", m.lastMacro, want)
	}
	for i, k := range want {
		if got := m.lastMacro[i].String(); got != k {
			t.Errorf("lastMacro[%d] = %q, want %q", i, got, k)
		}
	}

	// Replaying the finished macro on a fresh line must apply the recorded
	// edit exactly once, not recurse — there's no @ left in the sequence to
	// re-trigger executeMacroReplay.
	m = runKeys(t, m, fakeKey("down")) // move to "two"
	m = runKeys(t, m, m.lastMacro...)
	want2 := "xone\nxtwo\n"
	if got := m.buf.Content(); got != want2 {
		t.Errorf("buffer after replay = %q, want %q", got, want2)
	}
}

// TestMacroRecordToggleTwiceIsEmptyMacro verifies immediately starting and
// stopping recording (no keys typed in between) produces an empty, harmless
// macro rather than one containing stray state.
func TestMacroRecordToggleTwiceIsEmptyMacro(t *testing.T) {
	m := newTestModel("one\n")

	m = runKeys(t, m, fakeKey("q"), fakeKey("q"))
	if m.macroRecording {
		t.Error("macroRecording still true after immediate stop")
	}
	if len(m.lastMacro) != 0 {
		t.Errorf("lastMacro = %v, want empty", m.lastMacro)
	}
}
