package debugwin

import (
	"strings"
	"testing"
)

// A debug window on a stale server says so while it is open. It used to share
// the editor's stderr line, seen only once the window had closed.
func TestStaleServerPromptShowsAtStartup(t *testing.T) {
	f := newFake()
	f.stale = true
	m := started(t, f)
	if out := screen(m); !strings.Contains(out, "out of date") {
		t.Fatalf("no stale-server prompt:\n%s", out)
	}

	f2 := newFake()
	if out := screen(started(t, f2)); strings.Contains(out, "out of date") {
		t.Fatal("prompt shown for a current server")
	}
}

// Keys answer the prompt and nothing else: "c" here means continue-with-the-
// old-server, and must not also resume the program behind it.
func TestStaleServerPromptTakesKeysFirst(t *testing.T) {
	f := newFake()
	f.stale = true
	m := key(started(t, f), "c")
	if m.stale != nil {
		t.Fatal("c did not dismiss the prompt")
	}
	if len(f.controls) != 0 {
		t.Errorf("key reached the panels: controls sent %v", f.controls)
	}
	if !strings.Contains(screen(m), "main.handle") {
		t.Error("panels not shown after dismissing")
	}
	if m.quitNow {
		t.Error("continuing quit the window")
	}
}

func TestStaleServerPromptEnterQuits(t *testing.T) {
	f := newFake()
	f.stale = true
	m := key(started(t, f), "enter")
	if !m.quitNow {
		t.Fatal("Enter on the default choice did not quit")
	}
}
