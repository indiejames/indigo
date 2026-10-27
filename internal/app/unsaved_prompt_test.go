package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// unsavedApp has one buffer per path; dirty ones are opened as recovered
// content, which is how client.New marks a buffer unsaved.
func unsavedApp(paths []string, dirty map[string]bool) App {
	a := App{cfg: &config.Config{}, width: 100, height: 24, fileChangedIdx: -1}
	for i, p := range paths {
		a.buffers = append(a.buffers, client.New(&client.RPC{}, uint32(i+1), "x\n", 0, p, "/tmp", nil, dirty[p], 0))
	}
	return a
}

func letter(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestCtrlWOnAnUnsavedBufferShowsThePrompt(t *testing.T) {
	a := unsavedApp([]string{"/tmp/a.go"}, map[string]bool{"/tmp/a.go": true})
	updated, _ := a.Update(client.ConfirmCloseMsg{})
	a = updated.(App)
	if a.unsaved == nil || a.unsaved.quit {
		t.Fatalf("unsaved = %+v, want a close prompt", a.unsaved)
	}
	if frame, _ := a.renderFrame(); !strings.Contains(frame, "Save changes before closing?") {
		t.Error("the prompt is not drawn")
	}
}

func TestThePromptSwallowsKeysAndEscCancels(t *testing.T) {
	a := unsavedApp([]string{"/tmp/a.go"}, map[string]bool{"/tmp/a.go": true})
	a.unsaved = &unsavedPrompt{files: []string{"a.go"}}
	updated, cmd := a.Update(letter('x'))
	a = updated.(App)
	if cmd != nil || a.unsaved == nil {
		t.Fatal("a key the prompt does not answer reached the buffer or closed the prompt")
	}
	updated, cmd = a.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	a = updated.(App)
	if a.unsaved != nil || cmd != nil {
		t.Error("Esc did not cancel the prompt cleanly")
	}
}

// TestDontSaveClosesTheBuffer: d is the old :q!. It closes without saving,
// which the buffer reports as CloseBufferMsg once its close has run.
func TestDontSaveClosesTheBuffer(t *testing.T) {
	a := unsavedApp([]string{"/tmp/a.go"}, map[string]bool{"/tmp/a.go": true})
	a.unsaved = &unsavedPrompt{files: []string{"a.go"}}
	updated, cmd := a.Update(letter('d'))
	if updated.(App).unsaved != nil {
		t.Error("the prompt stayed open after d")
	}
	if cmd == nil {
		t.Fatal("d produced no command")
	}
}

func TestCtrlQWithNothingUnsavedQuitsAtOnce(t *testing.T) {
	a := unsavedApp([]string{"/tmp/a.go", "/tmp/b.go"}, nil)
	updated, cmd := a.Update(client.QuitRequestMsg{})
	if updated.(App).unsaved != nil {
		t.Error("asked about unsaved changes when there were none")
	}
	if cmd == nil {
		t.Error("Ctrl+Q with nothing unsaved did not start quitting")
	}
}

func TestCtrlQListsTheUnsavedFiles(t *testing.T) {
	a := unsavedApp([]string{"/tmp/a.go", "/tmp/b.go", "/tmp/c.go"},
		map[string]bool{"/tmp/a.go": true, "/tmp/c.go": true})
	updated, _ := a.Update(client.QuitRequestMsg{})
	p := updated.(App).unsaved
	if p == nil || !p.quit {
		t.Fatalf("unsaved = %+v, want a quit prompt", p)
	}
	if len(p.files) != 2 || p.files[0] != "a.go" || p.files[1] != "c.go" {
		t.Errorf("files = %v, want [a.go c.go]", p.files)
	}
}
