package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/staleprompt"
)

func staleApp(fromRecovery bool) App {
	cfg := &config.Config{}
	m := client.New(&client.RPC{}, 1, "hello\n", 0, "/tmp/a.txt", "/tmp", cfg, fromRecovery, 0)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return App{
		rpc:            &client.RPC{},
		buffers:        []client.Model{updated.(client.Model)},
		width:          80,
		height:         24,
		cfg:            cfg,
		fileChangedIdx: -1,
		jumpIdx:        -1,
		staleServer:    &staleprompt.Prompt{},
	}
}

func pressStale(a App, key string) (App, tea.Cmd) {
	var km tea.KeyPressMsg
	switch key {
	case "enter":
		km = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		km = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		km = tea.KeyPressMsg{Code: tea.KeyDown}
	default:
		km = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	m, cmd := a.Update(km)
	return m.(App), cmd
}

// The point of the prompt: the warning is on screen while the window is
// open. It used to be a stderr line printed under the alternate screen, seen
// only after quitting.
func TestStaleServerPromptIsDrawnOverTheFirstFrame(t *testing.T) {
	a := staleApp(false)
	frame, cur := a.renderFrame()
	if !strings.Contains(frame, "out of date") {
		t.Fatalf("frame does not show the stale-server prompt:\n%s", frame)
	}
	if cur != nil {
		t.Error("buffer cursor drawn through the prompt")
	}

	// A directory start has no buffer yet; the prompt must still show.
	a.buffers = nil
	if frame, _ := a.renderFrame(); !strings.Contains(frame, "out of date") {
		t.Fatalf("prompt missing when no buffer is open:\n%s", frame)
	}
}

// Keys meant for the editor must not reach it while the prompt is up: an "i"
// typed at the prompt would otherwise enter insert mode behind it.
func TestStaleServerPromptSwallowsEditorKeys(t *testing.T) {
	a := staleApp(false)
	before := a.buffers[0].View().Content
	a, cmd := pressStale(a, "i")
	if a.staleServer == nil {
		t.Fatal("prompt dismissed by an unrelated key")
	}
	if cmd != nil {
		t.Error("an unrelated key produced a command")
	}
	if a.buffers[0].View().Content != before {
		t.Error("key reached the buffer: its rendering changed")
	}
}

func TestStaleServerPromptEscContinues(t *testing.T) {
	a, cmd := pressStale(staleApp(false), "esc")
	if a.staleServer != nil {
		t.Fatal("esc did not dismiss the prompt")
	}
	if cmd != nil {
		t.Error("esc produced a command; continuing must not quit")
	}
	if frame, _ := a.renderFrame(); strings.Contains(frame, "out of date") {
		t.Error("prompt still drawn after dismissal")
	}
}

func TestStaleServerPromptContinueOption(t *testing.T) {
	a, _ := pressStale(staleApp(false), "down")
	a, cmd := pressStale(a, "enter")
	if a.staleServer != nil || cmd != nil {
		t.Errorf("Continue: staleServer=%v cmd=%v, want dismissed and no command", a.staleServer != nil, cmd != nil)
	}
}

// Quit is the default, so Enter on its own leaves.
func TestStaleServerPromptEnterQuitsByDefault(t *testing.T) {
	for _, key := range []string{"enter", "q"} {
		a, cmd := pressStale(staleApp(false), key)
		if a.staleServer != nil {
			t.Errorf("%s: prompt still showing", key)
		}
		// Not run: it closes buffers and disconnects over RPC, which needs a
		// live server. Its absence is what Continue and an unrelated key
		// assert; a quit is the only path here that returns one.
		if cmd == nil {
			t.Errorf("%s: no command; want the close-all-and-quit sequence", key)
		}
	}
}

// A buffer restored from a recovery file is dirty. Quitting from the prompt
// must not discard it; it refuses the way :qa does.
func TestStaleServerPromptQuitKeepsRecoveredWork(t *testing.T) {
	a := staleApp(true)
	if !a.buffers[0].Dirty() {
		t.Skip("a recovered buffer is not dirty; nothing to protect")
	}
	a, cmd := pressStale(a, "enter")
	if cmd != nil {
		t.Error("quit went ahead with a dirty recovered buffer")
	}
	// With one buffer there is no tab bar, so the refusal is moved into the
	// buffer's own status line (ensureStatusShown).
	if frame, _ := a.renderFrame(); !strings.Contains(frame, "unsaved") {
		t.Errorf("no unsaved-files refusal shown:\n%s", frame)
	}
}
