package dap

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAdapter is a scripted DAP adapter on the far end of a net.Pipe. onRequest
// is called for every request the client sends; it replies with the helpers.
type fakeAdapter struct {
	t    *testing.T
	conn net.Conn
	wMu  sync.Mutex
	seq  int

	mu       sync.Mutex
	received []message // everything the client sent, in order
}

func newFakeAdapter(t *testing.T, onRequest func(a *fakeAdapter, m message)) (*fakeAdapter, net.Conn) {
	t.Helper()
	clientEnd, adapterEnd := net.Pipe()
	a := &fakeAdapter{t: t, conn: adapterEnd}
	go func() {
		br := bufio.NewReader(adapterEnd)
		for {
			body, err := readFrame(br)
			if err != nil {
				return
			}
			var m message
			if json.Unmarshal(body, &m) != nil {
				continue
			}
			a.mu.Lock()
			a.received = append(a.received, m)
			a.mu.Unlock()
			if m.Type == "request" && onRequest != nil {
				go onRequest(a, m)
			}
		}
	}()
	t.Cleanup(func() { adapterEnd.Close() }) //nolint:errcheck
	return a, clientEnd
}

func (a *fakeAdapter) send(m message) {
	a.wMu.Lock()
	defer a.wMu.Unlock()
	a.seq++
	m.Seq = a.seq
	b, _ := json.Marshal(m)
	fmt.Fprintf(a.conn, "Content-Length: %d\r\n\r\n%s", len(b), b) //nolint:errcheck
}

func (a *fakeAdapter) respond(req message, body any) {
	raw, _ := json.Marshal(body)
	a.send(message{Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: true, Body: raw})
}

func (a *fakeAdapter) fail(req message, msg string, body any) {
	raw, _ := json.Marshal(body)
	a.send(message{Type: "response", RequestSeq: req.Seq, Command: req.Command, Success: false, Message: msg, Body: raw})
}

func (a *fakeAdapter) event(name string, body any) {
	raw, _ := json.Marshal(body)
	a.send(message{Type: "event", Event: name, Body: raw})
}

func (a *fakeAdapter) commands() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, m := range a.received {
		if m.Type == "request" {
			out = append(out, m.Command)
		}
	}
	return out
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestRequestResponseAndFailureDetail(t *testing.T) {
	_, conn := newFakeAdapter(t, func(a *fakeAdapter, m message) {
		switch m.Command {
		case "threads":
			a.respond(m, map[string]any{"threads": []Thread{{ID: 1, Name: "main"}}})
		case "evaluate":
			// Adapters put the useful part in body.error.format.
			a.fail(m, "evaluation failed", map[string]any{"error": map[string]any{"format": "undefined: y"}})
		}
	})
	c := NewClient(conn, nil)
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	threads, err := c.Threads(testCtx(t))
	if err != nil || len(threads) != 1 || threads[0].Name != "main" {
		t.Fatalf("Threads = %+v, %v", threads, err)
	}
	_, err = c.Evaluate(testCtx(t), "y", 0, EvalHover)
	if err == nil || !strings.Contains(err.Error(), "undefined: y") {
		t.Errorf("failed evaluate error = %v, want the adapter's detail", err)
	}
}

// Events arrive in order, and off the reader: a handler reacting to "stopped"
// by requesting the stack must not deadlock waiting for a response only the
// reader could deliver.
func TestEventsAreOrderedAndHandlersMayMakeRequests(t *testing.T) {
	a, conn := newFakeAdapter(t, func(a *fakeAdapter, m message) {
		if m.Command == "stackTrace" {
			a.respond(m, map[string]any{"stackFrames": []StackFrame{{ID: 7, Name: "main.main", Line: 12}}})
		}
	})
	var mu sync.Mutex
	var got []string
	gotFrame := make(chan StackFrame, 1)
	var c *Client
	c = NewClient(conn, func(e Event) {
		mu.Lock()
		got = append(got, e.Event)
		mu.Unlock()
		if e.Event == "stopped" {
			// Bounded, so a handler wrongly run on the reader times out and
			// the test fails, rather than deadlocking the whole run.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			frames, err := c.StackTrace(ctx, 1, 1)
			if err == nil && len(frames) == 1 {
				gotFrame <- frames[0]
			}
		}
	})
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	for _, name := range []string{"output", "stopped", "output", "continued"} {
		a.event(name, map[string]any{"threadId": 1})
	}
	select {
	case f := <-gotFrame:
		if f.Line != 12 {
			t.Errorf("frame line = %d, want 12", f.Line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a handler making a request deadlocked (was it run on the reader?)")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(got, ",") != "output,stopped,output,continued" {
		t.Errorf("events delivered as %v, want the order sent", got)
	}
}

// A reverse request is answered "not supported" — an adapter that asked is
// waiting for the reply.
func TestReverseRequestIsDeclined(t *testing.T) {
	a, conn := newFakeAdapter(t, nil)
	c := NewClient(conn, nil)
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	a.send(message{Type: "request", Command: "runInTerminal"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for _, m := range a.received {
			if m.Type == "response" && m.Command == "runInTerminal" {
				a.mu.Unlock()
				if m.Success {
					t.Error("reverse request answered success")
				}
				return
			}
		}
		a.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("reverse request was never answered")
}

// A request outstanding when the adapter goes away fails promptly rather than
// waiting out its context.
func TestPendingRequestFailsOnDisconnect(t *testing.T) {
	a, conn := newFakeAdapter(t, nil) // never answers
	c := NewClient(conn, nil)
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	errCh := make(chan error, 1)
	go func() {
		_, err := c.Threads(testCtx(t))
		errCh <- err
	}()
	time.Sleep(50 * time.Millisecond)
	a.conn.Close() //nolint:errcheck
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending request hung after the adapter disconnected")
	}
}

// launchAdapter is a fake adapter with the startup behaviour of a real one.
// withholdLaunch reproduces Delve and most others: launch is not answered
// until configurationDone. initializedFirst sends "initialized" as soon as
// launch arrives rather than after answering it.
func launchAdapter(t *testing.T, withholdLaunch, initializedFirst bool, launchFails bool) (*fakeAdapter, *Client) {
	var pendingLaunch *message
	var mu sync.Mutex
	a, conn := newFakeAdapter(t, func(a *fakeAdapter, m message) {
		switch m.Command {
		case "initialize":
			a.respond(m, Capabilities{SupportsConfigurationDoneRequest: true})
		case "launch":
			if launchFails {
				a.fail(m, "build failed", nil)
				return
			}
			if initializedFirst {
				a.event("initialized", nil)
			}
			if withholdLaunch {
				mu.Lock()
				pendingLaunch = &m
				mu.Unlock()
			} else {
				a.respond(m, nil)
			}
			if !initializedFirst {
				a.event("initialized", nil)
			}
		case "setBreakpoints":
			a.respond(m, map[string]any{"breakpoints": []Breakpoint{{Verified: true, Line: 5}}})
		case "configurationDone":
			a.respond(m, nil)
			mu.Lock()
			if pendingLaunch != nil {
				a.respond(*pendingLaunch, nil)
			}
			mu.Unlock()
		}
	})
	c := NewClient(conn, nil)
	t.Cleanup(func() { c.Close() }) //nolint:errcheck
	return a, c
}

// The handshake must work for each ordering real adapters use, and always
// in the order the spec requires: breakpoints before configurationDone.
func TestLaunchSessionOrdering(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		withholdLaunch, initializedFst bool
	}{
		{"launch answered only after configurationDone (Delve)", true, true},
		{"initialized sent before the launch response", false, true},
		{"launch answered first, initialized after", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, c := launchAdapter(t, tc.withholdLaunch, tc.initializedFst, false)
			ctx := testCtx(t)
			caps, err := c.Initialize(ctx, "go")
			if err != nil {
				t.Fatal(err)
			}
			err = c.LaunchSession(ctx, caps, map[string]any{"program": "."}, func(ctx context.Context) error {
				_, err := c.SetBreakpoints(ctx, "/w/main.go", []SourceBreakpoint{{Line: 5}})
				return err
			})
			if err != nil {
				t.Fatalf("LaunchSession: %v", err)
			}
			if got, want := strings.Join(a.commands(), ","), "initialize,launch,setBreakpoints,configurationDone"; got != want {
				t.Errorf("requests = %s, want %s", got, want)
			}
		})
	}
}

// A launch that fails (a build error) is reported, and configuration never runs.
func TestLaunchSessionReportsLaunchFailure(t *testing.T) {
	_, c := launchAdapter(t, false, false, true)
	ctx := testCtx(t)
	caps, _ := c.Initialize(ctx, "go")
	configured := false
	err := c.LaunchSession(ctx, caps, nil, func(context.Context) error { configured = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Errorf("err = %v, want the launch failure", err)
	}
	if configured {
		t.Error("configure ran after a failed launch")
	}
}

// Output sent ahead of a failure response is the explanation of the failure
// (Delve's build errors). After the request fails, WaitEventsIdle must not
// return until those events are handled — with a slow handler they are still
// queued when the response arrives, which made reading them racy.
func TestWaitEventsIdleCoversEventsBeforeAResponse(t *testing.T) {
	_, conn := newFakeAdapter(t, func(a *fakeAdapter, m message) {
		if m.Command == "launch" {
			a.event("output", OutputEvent{Category: "console", Output: "main.go:4: undefined: x\n"})
			a.event("output", OutputEvent{Category: "console", Output: "build failed\n"})
			a.fail(m, "Build error: Check the debug console for details.", nil)
		}
	})
	var mu sync.Mutex
	var seen []string
	c := NewClient(conn, func(e Event) {
		time.Sleep(50 * time.Millisecond) // a slow handler: events lag the response
		var o OutputEvent
		e.Decode(&o) //nolint:errcheck
		mu.Lock()
		seen = append(seen, o.Output)
		mu.Unlock()
	})
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	if _, err := c.Request(testCtx(t), "launch", nil); err == nil {
		t.Fatal("launch should have failed")
	}
	if err := c.WaitEventsIdle(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Errorf("handled %d of the 2 events sent before the failure: %q", len(seen), seen)
	}
}
