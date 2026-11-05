package app

import (
	"testing"

	tea "charm.land/bubbletea/v2"

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
	updated, _ := a.Update(reloadConflictMsg{idx: 1, bufID: 2})
	a2 := updated.(App)
	if a2.fileChangedIdx != 1 {
		t.Errorf("fileChangedIdx = %d, want 1: the conflict should ask the user", a2.fileChangedIdx)
	}
	if a2.status != "" {
		t.Errorf("status = %q, want none: a conflict is a question, not an error", a2.status)
	}
}

// TestReloadConflictForAMovedTabIsIgnored mirrors bufferReloadedMsg's check:
// a tab closed during the round trip shifts the indexes, so idx alone could
// name an unrelated buffer.
func TestReloadConflictForAMovedTabIsIgnored(t *testing.T) {
	a := twoTabApp()
	updated, _ := a.Update(reloadConflictMsg{idx: 1, bufID: 7})
	if got := updated.(App).fileChangedIdx; got != -1 {
		t.Errorf("fileChangedIdx = %d, want -1 for a buffer no longer at that index", got)
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
