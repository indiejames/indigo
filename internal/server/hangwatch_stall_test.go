package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/hangdetect"
	"github.com/indiejames/indigo/internal/proto"
)

// TestWedgedHandlerIsReportedWithItsVictims drives the whole mechanism against
// a real socket, a real serve() loop and a genuinely wedged handler, then reads
// the result back out of the real log.
//
// The wedge is the article rather than a simulation, and it is the shape that
// matters most. OpenFile reads the file with os.ReadFile and does not call
// call.Go(), so pointing it at a FIFO with no writer parks the handler inside
// open(2) *and* holds the connection's call queue — every later call on that
// connection waits behind it. A report naming only the handler that stalled
// would describe a quarter of that; the inventory is what shows the queue.
//
// (The FIFO technique is the one reload_buffer_test.go documents: opening it
// O_WRONLY|O_NONBLOCK fails with ENXIO until a reader has it open, which is how
// this knows the handler is genuinely parked rather than merely slow.)
func TestWedgedHandlerIsReportedWithItsVictims(t *testing.T) {
	t.Setenv("INDIGO_LOG_DIR", t.TempDir())
	// Short enough that the test does not wait out the shipped budget. See
	// serverCallBudget for why it is a var.
	old := serverCallBudget
	serverCallBudget = 500 * time.Millisecond
	t.Cleanup(func() { serverCallBudget = old })

	dir := t.TempDir()
	fifo := filepath.Join(dir, "stall.fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Skipf("cannot create a FIFO here: %v", err)
	}

	srv := startTestServer(t, dir)
	cl, _ := dialTestServer(t, srv)
	clientID := connectClient(t, cl)

	// A backstop for the case where the handler never reached the read at all
	// (the assertion below fails first, but the cleanup still has to not
	// block). O_NONBLOCK matters: a plain O_WRONLY open of a FIFO blocks until
	// a reader arrives, and by this point there may never be one again.
	t.Cleanup(func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close() //nolint:errcheck
		}
	})

	hangdetect.Start()
	t.Cleanup(hangdetect.Stop)
	start := time.Now()

	// Wedge the queue. This call never returns; the client giving up on its
	// own deadline does not unpark the handler, which is the whole reason the
	// server has to notice this for itself.
	//
	// Releasing an answer waits for it to resolve, so every release here is a
	// t.Cleanup rather than a defer — cleanups run last-registered-first, and
	// the one that unparks the handler is registered after all of them.
	wedgeCtx, cancelWedge := context.WithCancel(context.Background())
	t.Cleanup(cancelWedge)
	_, relWedge := cl.OpenFile(wedgeCtx, func(p proto.EditorService_openFile_Params) error {
		p.SetClientId(clientID)
		return p.SetPath(fifo)
	})
	t.Cleanup(relWedge)

	// Confirm the handler is actually parked in the read rather than merely
	// slow: until it has the FIFO open for reading, this fails with ENXIO.
	var writer *os.File
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			writer = w // deliberately left open: closing it signals EOF
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if writer == nil {
		t.Fatal("the handler never reached the read; the wedge did not take")
	}

	// A second, perfectly ordinary call. It cannot be served while the queue
	// is held, so it becomes the victim the inventory has to show.
	victimCtx, cancelVictim := context.WithCancel(context.Background())
	t.Cleanup(cancelVictim)
	_, relVictim := cl.BufferClientCount(victimCtx, func(p proto.EditorService_bufferClientCount_Params) error {
		p.SetBufferId(1)
		return nil
	})
	t.Cleanup(relVictim)

	// Registered last, so it runs first: closing the write end ends the read,
	// the handler returns, and every release above can then complete.
	t.Cleanup(func() { writer.Close() }) //nolint:errcheck

	report := waitForStallReport(t, start, 15*time.Second)
	if !strings.Contains(report, "kind=call_stalled") {
		t.Errorf("unexpected report kind: %s", report)
	}
	if !strings.Contains(report, "openFile") {
		t.Errorf("the report does not name the wedged handler: %s", report)
	}
	if !strings.Contains(report, "bufferClientCount") {
		t.Errorf("the report does not show the call queued behind it, which is "+
			"the half that identifies this as head-of-line blocking: %s", report)
	}
	if !strings.Contains(report, "goroutines follow") {
		t.Errorf("no goroutine dump, which is the part that names where it is stuck: %s", report)
	}
}

// waitForStallReport polls the shared log for the first stall report and
// returns it together with the lines that follow it, which is where the
// inventory and the dump live.
func waitForStallReport(t *testing.T, since time.Time, within time.Duration) string {
	t.Helper()
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		entries, err := debuglog.Read(debuglog.ReadOptions{Since: since.Add(-time.Minute)})
		if err != nil {
			t.Fatalf("debuglog.Read: %v", err)
		}
		var b strings.Builder
		found := false
		for _, e := range entries {
			if strings.Contains(e.Raw, hangdetect.Prefix) && strings.Contains(e.Raw, "phase=start") {
				found = true
			}
			if found {
				b.WriteString(e.Raw)
				b.WriteByte('\n')
			}
		}
		if found && strings.Contains(b.String(), "goroutines follow") {
			return b.String()
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("no stall report reached the log")
	return ""
}
