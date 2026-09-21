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

// TestUpdateBeatsBeforeDoingTheWork pins the ordering, which is the whole
// point of the heartbeat: a beat taken *after* the work has already been done
// records a message that by definition did not hang, and a message that does
// hang is never beaten for at all — so the detector would be silent in exactly
// the case it exists for.
//
// Observing it needs a vantage point inside the handler. App is a value
// receiver, so the fields it assigns are invisible from out here — but
// a.buffers is a slice, and the bufferReloadedMsg handler writes through its
// backing array, which the caller shares. So the beat reads that slot and
// records what it saw; if the handler had already run, it would see the
// replacement.
//
// An earlier version of this test asserted only that a beat happened and that
// the message was processed, which passes with the beat on either side of the
// work. Verified by moving the call in Update, which now fails here.
func TestUpdateBeatsBeforeDoingTheWork(t *testing.T) {
	a := newBeatTestApp() // buffers[0] is bufID 1 at /tmp/a.go

	var seen []string
	var atBeat string
	old := beat
	beat = func(_, what string, _ time.Duration) {
		seen = append(seen, what)
		atBeat = a.buffers[0].FilePath()
	}
	t.Cleanup(func() { beat = old })

	replacement := client.New(&client.RPC{}, 1, "", 0, "/tmp/replaced.go", "/tmp", nil, false, 0)
	a.Update(bufferReloadedMsg{idx: 0, oldBufID: 1, model: replacement})

	// The observation point is real: the handler did write through the slot
	// the beat was reading. Without this the assertion below would hold just
	// as well if nothing had happened at all.
	if got := a.buffers[0].FilePath(); got != "/tmp/replaced.go" {
		t.Fatalf("buffers[0] = %q; the handler never wrote, so this test proves nothing", got)
	}
	if atBeat != "/tmp/a.go" {
		t.Errorf("at beat time buffers[0] was %q, want the pre-handler %q — the beat "+
			"is being taken after the work rather than before it", atBeat, "/tmp/a.go")
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "bufferReloadedMsg") {
		t.Errorf("beats = %v, want one naming bufferReloadedMsg", seen)
	}

	// And the same holds for a message handled on a different branch.
	seen, atBeat = nil, ""
	updated, _ := a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if a2 := updated.(App); a2.width != 100 {
		t.Fatalf("the window-size message was not processed (width = %d)", a2.width)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "WindowSizeMsg") {
		t.Errorf("beats = %v, want one naming WindowSizeMsg", seen)
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
