package app

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// TestReportBufferStateAnswersForAnyTabNotJustTheActiveOne is why this is
// handled in App rather than client.Model: the generic dispatch routes to the
// active tab only, which would answer "no such buffer" for every backgrounded
// one and make a consistency check across tabs useless.
func TestReportBufferStateAnswersForAnyTabNotJustTheActiveOne(t *testing.T) {
	const content = "package main\n"
	a := App{
		buffers: []client.Model{
			client.New(&client.RPC{}, 1, "active\n", 0, "/tmp/a.go", "/tmp", &config.Config{}, false, 0),
			client.New(&client.RPC{}, 2, content, 0, "/tmp/b.go", "/tmp", &config.Config{}, false, 0),
		},
		active: 0,
		width:  80, height: 24,
		cfg: &config.Config{},
	}

	reply := make(chan client.BufferStateReport, 1)
	a.Update(client.ReportBufferStateMsg{BufID: 2, Reply: reply}) // the *background* tab

	select {
	case rep := <-reply:
		if !rep.Known {
			t.Fatal("Known = false for a buffer held in a background tab")
		}
		want := sha256.Sum256([]byte(content))
		if string(rep.ContentSha256) != string(want[:]) {
			t.Error("reported hash does not match that tab's content")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
}

func TestReportBufferStateReportsUnknownForAMissingBuffer(t *testing.T) {
	a := App{
		buffers: []client.Model{client.New(&client.RPC{}, 1, "x\n", 0, "/tmp/a.go", "/tmp", &config.Config{}, false, 0)},
		active:  0, width: 80, height: 24, cfg: &config.Config{},
	}
	reply := make(chan client.BufferStateReport, 1)
	a.Update(client.ReportBufferStateMsg{BufID: 99, Reply: reply})

	select {
	case rep := <-reply:
		if rep.Known {
			t.Error("Known = true for a buffer no tab holds")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
}

// TestReportBufferStateDoesNotBlockWithNoReader is the safety property: the
// callback abandons its wait when the server's timeout expires, so by the time
// the App answers there may be no reader. Blocking here would wedge the update
// loop — the editor would stop accepting keystrokes because a diagnostic asked
// it a question.
func TestReportBufferStateDoesNotBlockWithNoReader(t *testing.T) {
	a := App{
		buffers: []client.Model{client.New(&client.RPC{}, 1, "x\n", 0, "/tmp/a.go", "/tmp", &config.Config{}, false, 0)},
		active:  0, width: 80, height: 24, cfg: &config.Config{},
	}
	// Full buffer, nobody reading: the send must be dropped, not block.
	reply := make(chan client.BufferStateReport, 1)
	reply <- client.BufferStateReport{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		a.Update(client.ReportBufferStateMsg{BufID: 1, Reply: reply})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("App.Update blocked answering a consistency check with no reader — this would freeze the editor")
	}
}
