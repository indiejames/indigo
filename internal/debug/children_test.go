package debug

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/dap"
)

// childAdapter is a scripted TCP adapter shaped like js-debug: the first
// connection's launch asks the client, with startDebugging, for a second
// session, and that child session is the one that stops, has the stack and
// evaluates. The root answers stack requests with a frame of its own, so a
// request misrouted to it shows up in the result.
type childAdapter struct {
	ln   net.Listener
	mu   sync.Mutex
	conf map[string]any // the child's attach configuration
	bps  map[string]int // connection → breakpoints received
	eval []string       // how each evaluate was addressed
}

func newChildAdapter(t *testing.T) *childAdapter {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &childAdapter{ln: ln, bps: map[string]int{}}
	t.Cleanup(func() { ln.Close() }) //nolint:errcheck
	go func() {
		for n := 0; ; n++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			role := "root"
			if n > 0 {
				role = "child"
			}
			go a.serve(conn, role)
		}
	}()
	return a
}

type wire struct {
	mu  sync.Mutex
	c   net.Conn
	seq int
}

func (w *wire) send(m map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	m["seq"] = w.seq
	b, _ := json.Marshal(m)
	fmt.Fprintf(w.c, "Content-Length: %d\r\n\r\n%s", len(b), b) //nolint:errcheck
}

func (w *wire) respond(req map[string]any, body any) {
	w.send(map[string]any{"type": "response", "request_seq": req["seq"], "success": true, "command": req["command"], "body": body})
}

func (w *wire) event(name string, body any) {
	w.send(map[string]any{"type": "event", "event": name, "body": body})
}

func readMessage(r *bufio.Reader) (map[string]any, error) {
	n := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			n, _ = strconv.Atoi(v)
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(body, &m)
}

func (a *childAdapter) serve(conn net.Conn, role string) {
	w := &wire{c: conn}
	r := bufio.NewReader(conn)
	var launch map[string]any
	for {
		m, err := readMessage(r)
		if err != nil {
			return
		}
		if m["type"] == "response" { // our startDebugging, answered
			w.event("initialized", map[string]any{})
			continue
		}
		args, _ := m["arguments"].(map[string]any)
		switch m["command"] {
		case "initialize":
			w.respond(m, map[string]any{"supportsConfigurationDoneRequest": true})
		case "launch":
			launch = m
			w.send(map[string]any{"type": "request", "command": "startDebugging", "arguments": map[string]any{
				"request": "attach", "configuration": map[string]any{"__pendingTargetId": "t1"},
			}})
		case "attach":
			a.mu.Lock()
			a.conf = args
			a.mu.Unlock()
			launch = m
			w.event("initialized", map[string]any{})
		case "setBreakpoints":
			bps, _ := args["breakpoints"].([]any)
			a.mu.Lock()
			a.bps[role] += len(bps)
			a.mu.Unlock()
			var out []map[string]any
			for i, bp := range bps {
				line := bp.(map[string]any)["line"]
				out = append(out, map[string]any{"id": i + 1, "verified": role == "child", "line": line})
			}
			w.respond(m, map[string]any{"breakpoints": out})
		case "configurationDone":
			w.respond(m, nil)
			w.respond(launch, nil)
			if role == "child" {
				w.event("stopped", map[string]any{"reason": "breakpoint", "threadId": 0})
			}
		case "stackTrace":
			name := role + "Frame"
			w.respond(m, map[string]any{"stackFrames": []map[string]any{
				{"id": 0, "name": name, "line": 3, "column": 1, "source": map[string]any{"path": "/w/prog.ts"}},
			}})
		case "evaluate":
			how := "global"
			if f, ok := args["frameId"]; ok {
				how = fmt.Sprintf("frame %v", f)
			}
			a.mu.Lock()
			a.eval = append(a.eval, role+" "+how)
			a.mu.Unlock()
			w.respond(m, map[string]any{"result": how, "variablesReference": 0})
		case "threads":
			w.respond(m, map[string]any{"threads": []map[string]any{{"id": 0, "name": "main"}}})
		case "continue":
			w.respond(m, map[string]any{"allThreadsContinued": true})
			w.event("terminated", map[string]any{})
			go a.endRoot()
		case "disconnect":
			w.respond(m, nil)
			return
		default:
			w.respond(m, nil)
		}
		if role == "root" {
			a.mu.Lock()
			if rootWire == nil {
				rootWire = w
			}
			a.mu.Unlock()
		}
	}
}

// rootWire is the root connection's writer, for ending the session from the
// child's continue. One test uses childAdapter at a time.
var rootWire *wire

func (a *childAdapter) endRoot() {
	time.Sleep(20 * time.Millisecond) // the child's terminated first, as js-debug does
	a.mu.Lock()
	w := rootWire
	a.mu.Unlock()
	w.event("output", map[string]any{"category": "stderr", "output": "Process exited with code 3\r\n"})
	w.event("terminated", map[string]any{})
}

// A child session asked for with startDebugging becomes the target: the stop
// comes from it, stack, evaluation and stepping go to it, breakpoints are sent
// to it, and its ending does not end the session — the root's does.
func TestChildSessionIsTheTarget(t *testing.T) {
	rootWire = nil
	ad := newChildAdapter(t)
	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.startAdapter = func(ctx context.Context, _ Config, handler func(dap.Event)) (*dap.Client, error) {
		return dap.Dial(ctx, ad.ln.Addr().String(), handler)
	}
	m.SetAdapters(nil)
	m.ToggleBreakpoint("/w/prog.ts", 2)
	if err := m.Start(Config{Adapter: "lldb", Program: "/w/prog.ts"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 5*time.Second)
	if st.Path != "/w/prog.ts" || st.Line != 2 {
		t.Errorf("stopped at %s:%d", st.Path, st.Line)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) != 1 || frames[0].Name != "childFrame" {
		t.Fatalf("stack = %+v, %v; want the child's", frames, err)
	}
	// Frame 0 is a real frame here, so "the top frame" must be sent as 0,
	// not left out (which would evaluate globally).
	if v, err := m.Evaluate("x", 0, dap.EvalHover); err != nil || v.Value != "frame 0" {
		t.Errorf("evaluate = %+v, %v; want it addressed to frame 0", v, err)
	}
	bps, _ := m.Breakpoints.List("/w/prog.ts")
	ad.mu.Lock()
	gotConf, sentToChild, evals := ad.conf, ad.bps["child"], ad.eval
	ad.mu.Unlock()
	if gotConf["__pendingTargetId"] != "t1" {
		t.Errorf("child attached with %v, want the configuration from startDebugging", gotConf)
	}
	if sentToChild == 0 || len(bps) != 1 || !bps[0].Verified {
		t.Errorf("breakpoints: %d sent to the child, store %+v; want them sent and the child's answer kept", sentToChild, bps)
	}
	if len(evals) != 1 || evals[0] != "child frame 0" {
		t.Errorf("evaluations = %v, want one, on the child", evals)
	}

	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	end := waitState(t, m, StatusTerminated, 5*time.Second)
	for deadline := time.Now().Add(2 * time.Second); !end.HasExitCode && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
		end, _ = m.State()
	}
	if !end.HasExitCode || end.ExitCode != 3 {
		t.Errorf("ended as %+v, want exit code 3 from the root's report", end)
	}
}
