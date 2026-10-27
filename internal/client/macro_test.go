package client

import (
	"reflect"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
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

var (
	endKey  = tea.KeyPressMsg{Code: tea.KeyEnd}
	downKey = tea.KeyPressMsg{Code: tea.KeyDown}
)

// macroKeyNames is a macro as key names, for comparing.
func macroKeyNames(keys []tea.KeyMsg) []string {
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.String()
	}
	return names
}

func wantMacro(t *testing.T, m Model, want ...string) {
	t.Helper()
	if got := macroKeyNames(m.lastMacro); !slices.Equal(got, want) {
		t.Errorf("lastMacro = %q, want %q", got, want)
	}
}

// TestMacroRecordExcludesToggleKeys is a regression test for the core
// recording contract: the Ctrl+Space q sequences that start and stop
// recording must never themselves end up in the recorded sequence, only the
// keys typed in between.
func TestMacroRecordExcludesToggleKeys(t *testing.T) {
	m := modelessModel("one\ntwo\nthree\n")

	m = runKeys(t, m,
		ctrlSpace, fakeKey("q"), // start recording (excluded)
		endKey,                  // to the line end (recorded)
		fakeKey("!"),            // type '!' (recorded)
		ctrlSpace, fakeKey("q"), // stop recording (excluded)
	)

	if m.macroRecording {
		t.Fatal("macroRecording still true after stopping")
	}
	if len(m.macroKeys) != 0 {
		t.Errorf("macroKeys = %v, want cleared after stopping", m.macroKeys)
	}
	wantMacro(t, m, "end", "!")
	// The recorded edit itself must also have actually happened once, live.
	if got := m.buf.Content(); got != "one!\ntwo\nthree\n" {
		t.Errorf("buffer after recording = %q, want %q", got, "one!\ntwo\nthree\n")
	}
}

// TestMacroReplayReproducesRecordedEdit verifies that Ctrl+Space @ replays
// the recorded keys and reproduces the same edit on a different line.
func TestMacroReplayReproducesRecordedEdit(t *testing.T) {
	m := modelessModel("one\ntwo\nthree\n")

	m = runKeys(t, m, ctrlSpace, fakeKey("q"), endKey, fakeKey("!"), ctrlSpace, fakeKey("q"))
	if got := m.buf.Content(); got != "one!\ntwo\nthree\n" {
		t.Fatalf("setup: buffer = %q, want %q", got, "one!\ntwo\nthree\n")
	}

	// Move to line 1 ("two") and replay the recorded macro there.
	m = runKeys(t, m, downKey)
	if m.cursor.Line != 1 {
		t.Fatalf("setup: cursor.Line = %d, want 1", m.cursor.Line)
	}
	m = runKeys(t, m, ctrlSpace)
	out, cmd := m.Update(fakeKey("@"))
	m = out.(Model)
	m = runKeys(t, m, replayedKeys(t, cmd)...)

	want := "one!\ntwo!\nthree\n"
	if got := m.buf.Content(); got != want {
		t.Errorf("buffer after replay = %q, want %q", got, want)
	}
}

// replayedKeys runs the tea.Sequence executeMacroReplay returns and collects
// the keys it would deliver.
func replayedKeys(t *testing.T, cmd tea.Cmd) []tea.KeyMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("replay returned no command")
	}
	var keys []tea.KeyMsg
	var collect func(tea.Msg)
	collect = func(msg tea.Msg) {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			keys = append(keys, msg)
		case tea.BatchMsg:
			for _, c := range msg {
				if c != nil {
					collect(c())
				}
			}
		default:
			// tea.Sequence's message type is unexported; read its commands
			// through reflection rather than depend on its name.
			v := reflect.ValueOf(msg)
			if v.Kind() == reflect.Slice {
				for i := range v.Len() {
					if c, ok := v.Index(i).Interface().(tea.Cmd); ok && c != nil {
						collect(c())
					}
				}
			}
		}
	}
	collect(cmd())
	return keys
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

// TestMacroRecordsMenuLettersTypedOutsideTheMenu: q and @ are only macro
// commands inside the Command menu. Typed on their own they are text, and
// are recorded like any other key.
func TestMacroRecordsMenuLettersTypedOutsideTheMenu(t *testing.T) {
	m := modelessModel("one\n")

	m = runKeys(t, m,
		ctrlSpace, fakeKey("q"), // start recording
		fakeKey("q"),            // literal 'q' — must NOT stop recording
		fakeKey("@"),            // literal '@' — must NOT replay
		ctrlSpace, fakeKey("q"), // stop recording
	)

	if m.macroRecording {
		t.Fatal("recording should have stopped")
	}
	if got := m.buf.Content(); got != "q@one\n" {
		t.Fatalf("buffer = %q, want %q (literal q and @ typed)", got, "q@one\n")
	}
	wantMacro(t, m, "q", "@")
}

// TestMacroExcludesRejectedReplayFromRecording is a regression test for a
// self-referential-replay bug: asking for a replay while a recording is in
// progress is rejected by executeMacroReplay — but without
// macroReplayBlocked's special handling, the keys asking for it would still
// get *recorded* into the macro. Later, replaying the finished macro would
// redeliver them — no longer "while recording" at that point, so they would
// actually run — scheduling another full replay of the same macro, which
// itself contains the same keys, recursing without end.
func TestMacroExcludesRejectedReplayFromRecording(t *testing.T) {
	m := modelessModel("one\ntwo\n")

	m = runKeys(t, m,
		ctrlSpace, fakeKey("q"), // start recording
		fakeKey("x"),            // literal 'x'
		ctrlSpace, fakeKey("@"), // replay attempt while recording — rejected, must NOT be recorded
		ctrlSpace, fakeKey("q"), // stop recording
	)

	if m.macroRecording {
		t.Fatal("recording should have stopped")
	}
	if m.macroReplayBlocked {
		t.Error("macroReplayBlocked left set — should be cleared the moment it's consumed")
	}
	wantMacro(t, m, "x")

	// Replaying the finished macro on a fresh line must apply the recorded
	// edit exactly once, not recurse.
	m = runKeys(t, m, downKey, tea.KeyPressMsg{Code: tea.KeyHome})
	m = runKeys(t, m, m.lastMacro...)
	if want := "xone\nxtwo\n"; m.buf.Content() != want {
		t.Errorf("buffer after replay = %q, want %q", m.buf.Content(), want)
	}
}

// TestMacroRecordToggleTwiceIsEmptyMacro verifies immediately starting and
// stopping recording (no keys typed in between) produces an empty, harmless
// macro rather than one containing stray state.
func TestMacroRecordToggleTwiceIsEmptyMacro(t *testing.T) {
	m := modelessModel("one\n")

	m = runKeys(t, m, ctrlSpace, fakeKey("q"), ctrlSpace, fakeKey("q"))
	if m.macroRecording {
		t.Error("macroRecording still true after immediate stop")
	}
	if len(m.lastMacro) != 0 {
		t.Errorf("lastMacro = %v, want empty", m.lastMacro)
	}
}

// TestMacroKeepsMenuCommandsItRecorded: a menu command recorded in a macro
// stays in it; only the menu sequence that stops the recording is cut.
func TestMacroKeepsMenuCommandsItRecorded(t *testing.T) {
	m := modelessModel("one\n")

	m = runKeys(t, m,
		ctrlSpace, fakeKey("q"),
		ctrlSpace, fakeKey("g"), fakeKey("e"), // go to end of file (recorded)
		ctrlSpace, fakeKey("q"),
	)
	wantMacro(t, m, "ctrl+space", "g", "e")
}
