package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/hangdetect"
)

type beatRecord struct {
	tag       string
	what      string
	threshold time.Duration
}

func captureBeats(t *testing.T) *[]beatRecord {
	t.Helper()
	var got []beatRecord
	old := beat
	beat = func(tag, what string, threshold time.Duration) {
		got = append(got, beatRecord{tag, what, threshold})
	}
	t.Cleanup(func() { beat = old })
	return &got
}

func newBeatTestApp() App {
	return App{
		buffers: []client.Model{client.New(&client.RPC{}, 1, "", 0, "/tmp/a.go", "/tmp", nil, false, 0)},
		active:  0,
		width:   80,
		height:  24,
		cfg:     &config.Config{},
	}
}

// TestUpdateBeatsTheHangDetector is the dispatch half of the pair. The
// detector's own tests prove it reports a loop that stops beating; nothing in
// them would notice that Update never beats in the first place — and a
// heartbeat nobody sends reports silence, which is indistinguishable from
// health. Verified by deleting the call in Update, which fails only this test.
func TestUpdateBeatsTheHangDetector(t *testing.T) {
	got := captureBeats(t)
	a := newBeatTestApp()

	a.Update(bufferReloadedMsg{idx: 99, oldBufID: 1})

	if len(*got) != 1 {
		t.Fatalf("Update produced %d beats, want exactly 1", len(*got))
	}
	b := (*got)[0]
	if b.tag != UILoopTag {
		t.Errorf("tag = %q, want %q", b.tag, UILoopTag)
	}
	if b.what != "app.bufferReloadedMsg" {
		t.Errorf("what = %q, want the message type Update is about to process", b.what)
	}
	if b.threshold != hangdetect.LoopThreshold {
		t.Errorf("threshold = %v, want %v", b.threshold, hangdetect.LoopThreshold)
	}
}

// TestUpdateBeatsBeforeDoingTheWork pins the ordering that makes a report
// usable: the beat has to name the message that is about to be handled, not
// the one that was handled last. Beating afterwards would name the previous
// message on every stall — always the wrong one.
func TestUpdateBeatsBeforeDoingTheWork(t *testing.T) {
	var seen []string
	old := beat
	beat = func(_, what string, _ time.Duration) { seen = append(seen, what) }
	t.Cleanup(func() { beat = old })

	a := newBeatTestApp()
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a2 := updated.(App)
	if a2.width != 100 {
		t.Fatalf("the message was not actually processed (width = %d)", a2.width)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "WindowSizeMsg") {
		t.Fatalf("beats = %v, want one naming WindowSizeMsg", seen)
	}
}

func TestMsgNameDescribesTypeAndNotContents(t *testing.T) {
	if got := msgName(bufferOpenedMsg{}); got != "app.bufferOpenedMsg" {
		t.Errorf("msgName = %q, want %q", got, "app.bufferOpenedMsg")
	}
	if got := msgName(&bufferOpenedMsg{}); got != "app.bufferOpenedMsg" {
		t.Errorf("msgName of a pointer = %q, want the same as the value", got)
	}
	if got := msgName(nil); got != "nil" {
		t.Errorf("msgName(nil) = %q, want %q", got, "nil")
	}

	// A key press must not put the typed character in the log — this output
	// lands in a shared temp directory and gets pasted into bug reports, and
	// "a key press froze it" is the distinction worth having.
	got := msgName(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if strings.Contains(got, "x'") || strings.Contains(got, `"x"`) {
		t.Errorf("msgName leaked the keystroke: %q", got)
	}
	if !strings.Contains(got, "KeyPressMsg") {
		t.Errorf("msgName = %q, want it to name the key-press type", got)
	}
}
