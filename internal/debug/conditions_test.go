package debug

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/indiejames/indigo/internal/dap"
	"github.com/indiejames/indigo/internal/document"
)

// Set creates a breakpoint or changes the one there; a change must be
// confirmed again by the adapter, and an unchanged Set is not a change.
func TestSetBreakpointConditionAndLog(t *testing.T) {
	b := NewBreakpoints()
	if !b.Set("/w/a.go", 4, "i > 2", "") {
		t.Fatal("Set on an empty line should report a change")
	}
	b.setResults("/w/a.go", nil, []int{4}, []resultLine{{verified: true, line: 4}})
	seq := b.Seq()
	if b.Set("/w/a.go", 4, "i > 2", "") || b.Seq() != seq {
		t.Error("an identical Set counted as a change")
	}
	if !b.Set("/w/a.go", 4, "i > 2", "i={i}") {
		t.Error("adding a log message is a change")
	}
	bps, _ := b.List("/w/a.go")
	if len(bps) != 1 || bps[0].Condition != "i > 2" || bps[0].LogMessage != "i={i}" || bps[0].Verified {
		t.Errorf("got %+v, want one unverified logpoint with its condition", bps)
	}
	// The condition travels with its line through an edit above it.
	b.ApplyEdit("/w/a.go", document.Op{Type: document.OpInsert, InsertLine: 0, InsertText: "x\n"})
	if bps, _ := b.List("/w/a.go"); bps[0].Line != 5 || bps[0].Condition != "i > 2" {
		t.Errorf("after an insert above: %+v", bps)
	}
	// Toggling removes it whatever it carries.
	if b.Toggle("/w/a.go", 5) {
		t.Error("Toggle on a conditional breakpoint should remove it")
	}
}

// scriptedAdapter answers setBreakpoints by verifying every breakpoint it was
// sent, and records the requests.
type scriptedAdapter struct {
	mu   sync.Mutex
	sent [][]dap.SourceBreakpoint
}

func (s *scriptedAdapter) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	seq := 0
	for {
		n := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
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
			return
		}
		var req struct {
			Seq       int    `json:"seq"`
			Command   string `json:"command"`
			Arguments struct {
				Breakpoints []dap.SourceBreakpoint `json:"breakpoints"`
			} `json:"arguments"`
		}
		json.Unmarshal(body, &req) //nolint:errcheck
		resp := map[string]any{"type": "response", "request_seq": req.Seq, "success": true, "command": req.Command}
		if req.Command == "setBreakpoints" {
			s.mu.Lock()
			s.sent = append(s.sent, req.Arguments.Breakpoints)
			s.mu.Unlock()
			var bps []map[string]any
			for _, bp := range req.Arguments.Breakpoints {
				bps = append(bps, map[string]any{"verified": true, "line": bp.Line})
			}
			resp["body"] = map[string]any{"breakpoints": bps}
		}
		seq++
		resp["seq"] = seq
		out, _ := json.Marshal(resp)
		if _, err := fmt.Fprintf(conn, "Content-Length: %d\r\n\r\n%s", len(out), out); err != nil {
			return
		}
	}
}

// A condition or log message the adapter does not support is not sent — a
// conditional breakpoint would stop every time, a logpoint would stop instead
// of printing — and the breakpoint says why. Supported ones go through with
// their condition and message.
func TestUnsupportedConditionsAreNotSent(t *testing.T) {
	clientEnd, adapterEnd := net.Pipe()
	ad := &scriptedAdapter{}
	go ad.serve(adapterEnd)
	c := dap.NewClient(clientEnd, func(dap.Event) {})
	t.Cleanup(func() { c.Close() }) //nolint:errcheck

	m := NewManager(nil)
	m.Breakpoints.Toggle("/w/a.go", 1)
	m.Breakpoints.Set("/w/a.go", 3, "i > 2", "")
	m.Breakpoints.Set("/w/a.go", 5, "", "i={i}")

	ctx := t.Context()
	if err := m.sendBreakpoints(ctx, c, "/w/a.go"); err != nil {
		t.Fatal(err)
	}
	bps, _ := m.Breakpoints.List("/w/a.go")
	if !bps[0].Verified || bps[1].Verified || bps[2].Verified {
		t.Errorf("with no support, only the plain breakpoint should be set: %+v", bps)
	}
	if !strings.Contains(bps[1].Message, "conditional") || !strings.Contains(bps[2].Message, "logpoints") {
		t.Errorf("reasons = %q, %q", bps[1].Message, bps[2].Message)
	}
	ad.mu.Lock()
	if len(ad.sent) != 1 || len(ad.sent[0]) != 1 || ad.sent[0][0].Line != 2 {
		t.Errorf("sent %+v, want only the plain breakpoint (line 2)", ad.sent)
	}
	ad.mu.Unlock()

	m.mu.Lock()
	m.caps = dap.Capabilities{SupportsConditionalBreakpoints: true, SupportsLogPoints: true}
	m.mu.Unlock()
	if err := m.sendBreakpoints(ctx, c, "/w/a.go"); err != nil {
		t.Fatal(err)
	}
	bps, _ = m.Breakpoints.List("/w/a.go")
	for _, bp := range bps {
		if !bp.Verified {
			t.Errorf("with support, %+v should be set", bp)
		}
	}
	ad.mu.Lock()
	last := ad.sent[len(ad.sent)-1]
	ad.mu.Unlock()
	if len(last) != 3 || last[1].Condition != "i > 2" || last[2].LogMessage != "i={i}" {
		t.Errorf("sent %+v, want the condition and log message through", last)
	}
}

const loopDebuggee = `package main

import "fmt"

func main() {
	total := 0
	for i := 0; i < 5; i++ {
		total += i // line 8 (0-based 7): logpoint
		fmt.Println("step", i) // line 9 (0-based 8): conditional
	}
	fmt.Println("total", total)
}
`

// Against real Delve: a conditional breakpoint stops only when its condition
// holds, and a logpoint prints — interpolating {expressions} — without
// stopping.
func TestConditionalBreakpointAndLogpointAgainstDelve(t *testing.T) {
	canDebug(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte(loopDebuggee), 0o644)                                    //nolint:errcheck
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n\ngo 1.21\n"), 0o644) //nolint:errcheck

	m := NewManager(nil)
	t.Cleanup(m.Shutdown)
	m.SetBreakpoint(main, 7, "", "loop i={i} total={total}")
	m.SetBreakpoint(main, 8, "i == 3", "")
	if err := m.Start(Config{Adapter: "go", Program: dir, Cwd: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := waitState(t, m, StatusStopped, 60*time.Second)
	if st.Line != 8 {
		t.Fatalf("stopped at line %d, want 9 (the conditional breakpoint, never the logpoint)", st.Line+1)
	}
	frames, err := m.StackTrace(0)
	if err != nil || len(frames) == 0 {
		t.Fatal(err)
	}
	if v, err := m.Evaluate("i", frames[0].ID, dap.EvalHover); err != nil || v.Value != "3" {
		t.Errorf("stopped with i = %+v, %v; want 3", v, err)
	}
	bps, _ := m.Breakpoints.List(main)
	for _, bp := range bps {
		if !bp.Verified {
			t.Errorf("%+v not verified by Delve", bp)
		}
	}
	if err := m.Control(ActionContinue); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StatusTerminated, 30*time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		chunks, _, _ := m.Output(0)
		var out strings.Builder
		for _, c := range chunks {
			out.WriteString(c.Text)
		}
		s := out.String()
		if strings.Contains(s, "loop i=0 total=0") && strings.Contains(s, "loop i=4 total=6") && strings.Contains(s, "total 10") {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("logpoint output missing: %q", s)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}
