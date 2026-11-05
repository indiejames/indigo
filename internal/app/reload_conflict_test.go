package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func twoTabApp() App {
	return App{
		buffers: []client.Model{
			newReloadTestModel(1, "/tmp/a.go"),
			newReloadTestModel(2, "/tmp/b.go"),
		},
		width:          80,
		height:         24,
		cfg:            &config.Config{},
		fileChangedIdx: -1,
	}
}

// TestReloadConflictShowsTheFileChangedPrompt: when the server declines a
// reload because an edit landed during its read, the buffer holds changes the
// file does not. That used to be reported as an error on the tab bar and left
// there, so the window kept showing the old text with no way to act on it.
func TestReloadConflictShowsTheFileChangedPrompt(t *testing.T) {
	a := twoTabApp()
	updated, _ := a.Update(reloadConflictMsg{bufID: 2})
	a2 := updated.(App)
	if a2.fileChangedIdx != 1 {
		t.Errorf("fileChangedIdx = %d, want 1: the conflict should ask the user", a2.fileChangedIdx)
	}
	if a2.status != "" {
		t.Errorf("status = %q, want none: a conflict is a question, not an error", a2.status)
	}
	if got := a2.fileChangedName(); got != "b.go" {
		t.Errorf("prompt names %q, want b.go: it is not the active tab, so it must say which file", got)
	}
}

// TestReloadConflictFindsItsBufferAfterTabsShift: the conflict names its buffer
// by BufID, so a tab closed during the round trip does not lose it, and a
// buffer that has since closed asks nothing.
func TestReloadConflictFindsItsBufferAfterTabsShift(t *testing.T) {
	a := twoTabApp()
	a.buffers = a.buffers[1:] // tab a.go closed; b.go is now at index 0
	updated, _ := a.Update(reloadConflictMsg{bufID: 2})
	if got := updated.(App).fileChangedIdx; got != 0 {
		t.Errorf("fileChangedIdx = %d, want 0, where b.go now is", got)
	}
	updated, _ = a.Update(reloadConflictMsg{bufID: 7})
	if got := updated.(App).fileChangedIdx; got != -1 {
		t.Errorf("fileChangedIdx = %d, want -1 for a buffer no longer open", got)
	}
}

// TestASecondConflictWaitsForTheOpenPrompt: retargeting an open prompt would
// make Enter reload a different file from the one the user was reading about.
func TestASecondConflictWaitsForTheOpenPrompt(t *testing.T) {
	a := twoTabApp()
	updated, _ := a.Update(reloadConflictMsg{bufID: 1})
	updated, _ = updated.(App).Update(reloadConflictMsg{bufID: 2})
	a = updated.(App)
	if a.fileChangedIdx != 0 {
		t.Fatalf("fileChangedIdx = %d, want 0: the open prompt keeps its file", a.fileChangedIdx)
	}
	// Keep local for a.go; the prompt then asks about b.go.
	updated, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	updated, _ = updated.(App).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	a = updated.(App)
	if a.fileChangedIdx != 1 || a.fileChangedSel != 0 {
		t.Errorf("after answering: fileChangedIdx = %d, sel = %d, want 1 and 0", a.fileChangedIdx, a.fileChangedSel)
	}
	updated, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := updated.(App).fileChangedIdx; got != -1 {
		t.Errorf("after the last answer: fileChangedIdx = %d, want -1", got)
	}
}

// TestAPasteDoesNotReachWhatIsBehindThePrompt mirrors the stale-server
// prompt: a paste is its own message type, so it skipped the key intercept
// and landed in whatever took text behind the prompt, here an input dialog.
func TestAPasteDoesNotReachWhatIsBehindThePrompt(t *testing.T) {
	a := twoTabApp()
	a.pluginInput = &appPluginInput{title: "t", width: 80, height: 24}
	a.fileChangedIdx = 0
	updated, _ := a.Update(tea.PasteMsg{Content: "pasted"})
	if got := updated.(App).pluginInput.text; got != "" {
		t.Errorf("input behind the prompt got %q; the paste should be dropped", got)
	}
}

// TestReloadErrorsMapToTheRightMessage covers doReloadBuffer's error handling:
// the server's conflict text asks the user, and any other error is shown,
// including a read error whose path happens to contain the same words.
func TestReloadErrorsMapToTheRightMessage(t *testing.T) {
	conflict := errString("editor.capnp:EditorService.reloadBuffer: buffer 6 changed while reloading; try again")
	if got, ok := reloadErrorMsg(6, conflict).(reloadConflictMsg); !ok || got.bufID != 6 {
		t.Errorf("conflict mapped to %#v, want reloadConflictMsg{bufID: 6}", reloadErrorMsg(6, conflict))
	}
	readErr := errString("read /tmp/buffer 6 changed while reloading; try again/a.go: permission denied")
	if _, ok := reloadErrorMsg(6, readErr).(errorOpenMsg); !ok {
		t.Errorf("read error mapped to %#v, want errorOpenMsg", reloadErrorMsg(6, readErr))
	}
}

// TestAKeyPressClearsTheTabBarStatus is the regression test for an error that
// stayed on the tab bar for the rest of the session: only a tab click or a
// group switch cleared the App's status.
func TestAKeyPressClearsTheTabBarStatus(t *testing.T) {
	a := twoTabApp()
	updated, _ := a.Update(errorOpenMsg{err: errString("buffer 6 changed while reloading; try again")})
	a = updated.(App)
	if !a.showTabBar() || a.status == "" {
		t.Fatalf("setup: want the status on a visible tab bar, got status %q, tab bar %v", a.status, a.showTabBar())
	}
	updated, _ = a.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if got := updated.(App).status; got != "" {
		t.Errorf("status after a key press = %q, want it cleared", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// TestThePromptIsDrawnOverAFullScreenView: the prompt takes keys before the
// file picker, grep and diagnostics views do, so it must be drawn over them
// too, or Enter would reload a file the user was never asked about.
func TestThePromptIsDrawnOverAFullScreenView(t *testing.T) {
	a := twoTabApp()
	a.grep = &grepPicker{width: 80, height: 24}
	updated, _ := a.Update(reloadConflictMsg{bufID: 2})
	frame, _ := updated.(App).renderFrame()
	if !strings.Contains(ansi.Strip(frame), "b.go changed on disk") {
		t.Errorf("the prompt is not drawn over the grep view; frame:\n%s", ansi.Strip(frame))
	}
}
