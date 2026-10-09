package debugwin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/rpcclient"
)

// fakeBackend is a scripted server: a stopped session with two frames.
type fakeBackend struct {
	mu        sync.Mutex
	state     rpcclient.DebugState
	frames    []rpcclient.DebugFrame
	scopes    map[int64][]rpcclient.DebugScope
	vars      map[int64][]rpcclient.DebugVariable
	evals     map[string]string // "frame:expr" → value
	output    []rpcclient.DebugOutputChunk
	controls  []rpcclient.DebugAction
	restarts  int
	opened    []string
	varsCalls int
	stale     bool
}

func newFake() *fakeBackend {
	return &fakeBackend{
		state: rpcclient.DebugState{Status: rpcclient.DebugStopped, Seq: 1, Path: "/w/main.go", Line: 9, Reason: "breakpoint"},
		frames: []rpcclient.DebugFrame{
			{ID: 1, Name: "main.handle", Path: "/w/main.go", Line: 9},
			{ID: 2, Name: "main.main", Path: "/w/main.go", Line: 20},
		},
		scopes: map[int64][]rpcclient.DebugScope{
			1: {{Name: "Locals", Ref: 10}, {Name: "Globals", Ref: 11, Expensive: true}},
			2: {{Name: "Locals", Ref: 20}},
		},
		vars: map[int64][]rpcclient.DebugVariable{
			10: {{Name: "x", Value: "42", Type: "int"}, {Name: "cfg", Value: "{...}", Type: "Config", Ref: 12}},
			12: {{Name: "Name", Value: `"svc"`, Type: "string"}},
			20: {{Name: "args", Value: "[]string len: 0"}},
		},
		evals: map[string]string{"1:x + 1": "43", "2:x + 1": "?"},
	}
}

func (f *fakeBackend) DebugState(context.Context) (rpcclient.DebugState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}
func (f *fakeBackend) DebugStackTrace(context.Context, int64) ([]rpcclient.DebugFrame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames, nil
}
func (f *fakeBackend) DebugScopes(_ context.Context, id int64) ([]rpcclient.DebugScope, error) {
	return f.scopes[id], nil
}
func (f *fakeBackend) DebugVariables(_ context.Context, ref int64) ([]rpcclient.DebugVariable, error) {
	f.mu.Lock()
	f.varsCalls++
	f.mu.Unlock()
	return f.vars[ref], nil
}
func (f *fakeBackend) DebugEvaluate(_ context.Context, expr string, frame int64, _ string) (rpcclient.DebugVariable, error) {
	if v, ok := f.evals[fmt.Sprintf("%d:%s", frame, expr)]; ok {
		return rpcclient.DebugVariable{Value: v}, nil
	}
	return rpcclient.DebugVariable{}, errors.New("undefined: " + expr)
}
func (f *fakeBackend) DebugOutput(_ context.Context, since uint64) ([]rpcclient.DebugOutputChunk, uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []rpcclient.DebugOutputChunk
	var latest uint64
	for _, c := range f.output {
		if c.Seq > since {
			out = append(out, c)
		}
		latest = max(latest, c.Seq)
	}
	return out, latest, false, nil
}
func (f *fakeBackend) DebugControl(_ context.Context, a rpcclient.DebugAction) error {
	f.mu.Lock()
	f.controls = append(f.controls, a)
	f.mu.Unlock()
	return nil
}
func (f *fakeBackend) DebugRestart(_ context.Context, fallback rpcclient.DebugConfig) (rpcclient.DebugConfig, error) {
	f.mu.Lock()
	f.restarts++
	f.mu.Unlock()
	return rpcclient.DebugConfig{Name: "server"}, errors.New("build failed")
}
func (f *fakeBackend) RequestOpenFile(_ context.Context, path string, line uint32) error {
	f.mu.Lock()
	f.opened = append(f.opened, fmt.Sprintf("%s:%d", path, line))
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) ServerStale() bool { return f.stale }

// drive runs cmd and feeds its messages back into m, following batches.
func drive(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = drive(m, c)
		}
		return m
	}
	if msg == nil {
		return m
	}
	updated, next := m.Update(msg)
	return drive(updated.(Model), next)
}

func key(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "up":
			msg = tea.KeyPressMsg{Code: tea.KeyUp}
		case "left":
			msg = tea.KeyPressMsg{Code: tea.KeyLeft}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		updated, cmd := m.Update(msg)
		m = drive(updated.(Model), cmd)
	}
	return m
}

func started(t *testing.T, f *fakeBackend) Model {
	t.Helper()
	m := New(f)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return drive(updated.(Model), updated.(Model).Init())
}

func screen(m Model) string { return ansi.Strip(m.render()) }

// On a stop the window shows the stack, opens locals by itself (but not an
// expensive scope like globals), and says where the program stopped.
func TestStopShowsStackAndLocals(t *testing.T) {
	m := started(t, newFake())
	out := screen(m)
	for _, want := range []string{"breakpoint", "main.go:10", "main.handle", "main.main", "x = 42", "▸ cfg"} {
		if !strings.Contains(out, want) {
			t.Errorf("screen lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "▾ Globals") {
		t.Error("an expensive scope opened by itself")
	}
}

// Opening a struct fetches its fields, and it stays open at the next stop —
// re-expanding the same variable after every step would make stepping useless.
func TestExpansionSurvivesTheNextStop(t *testing.T) {
	f := newFake()
	m := started(t, f)
	m = key(m, "tab")                   // Variables
	m = key(m, "down", "down", "enter") // Locals, x, cfg → open cfg
	if out := screen(m); !strings.Contains(out, `Name = "svc"`) {
		t.Fatalf("cfg did not open:\n%s", out)
	}
	// The program steps and stops again.
	f.mu.Lock()
	f.state.Seq = 2
	f.mu.Unlock()
	updated, cmd := m.Update(rpcclient.DebugChangedMsg{StateSeq: 2})
	m = drive(updated.(Model), cmd)
	if out := screen(m); !strings.Contains(out, `Name = "svc"`) {
		t.Errorf("cfg closed again after the next stop:\n%s", out)
	}
}

// Choosing another frame shows its variables and moves the editor windows to it.
func TestSelectingAFrameShowsItsVariablesAndRevealsIt(t *testing.T) {
	f := newFake()
	m := started(t, f)
	m = key(m, "down", "enter") // Call Stack is focused first; pick main.main
	out := screen(m)
	if !strings.Contains(out, "args = []string") || strings.Contains(out, "x = 42") {
		t.Errorf("variables are not main.main's:\n%s", out)
	}
	if len(f.opened) != 1 || f.opened[0] != "/w/main.go:20" {
		t.Errorf("editor asked to open %v, want main.go:20", f.opened)
	}
}

func TestWatches(t *testing.T) {
	f := newFake()
	m := started(t, f)
	m = key(m, "a", "x", " ", "+", " ", "1", "enter")
	if out := screen(m); !strings.Contains(out, "x + 1 = 43") {
		t.Fatalf("watch not evaluated:\n%s", out)
	}
	m = key(m, "a", "n", "o", "p", "e", "enter")
	if out := screen(m); !strings.Contains(out, "undefined: nope") {
		t.Errorf("a failing watch does not show why:\n%s", out)
	}
	// Watches re-evaluate in the frame you choose.
	m.focus = secStack
	m = key(m, "down", "enter")
	if out := screen(m); !strings.Contains(out, "x + 1 = ?") {
		t.Errorf("watch not re-evaluated in the chosen frame:\n%s", out)
	}
	// d deletes the selected watch.
	m.focus, m.cursor[secWatches] = secWatches, 1
	m = key(m, "d")
	if len(m.watches) != 1 || m.watches[0].expr != "x + 1" {
		t.Errorf("watches after delete = %+v", m.watches)
	}
}

// Output arrives in chunks that do not respect line boundaries.
func TestOutputAcrossChunks(t *testing.T) {
	f := newFake()
	f.output = []rpcclient.DebugOutputChunk{{Seq: 1, Text: "hel"}, {Seq: 2, Text: "lo\nwor"}}
	m := started(t, f)
	f.mu.Lock()
	f.output = append(f.output, rpcclient.DebugOutputChunk{Seq: 3, Text: "ld\n"})
	f.mu.Unlock()
	updated, cmd := m.Update(rpcclient.DebugChangedMsg{StateSeq: 1, OutputSeq: 3})
	m = drive(updated.(Model), cmd)
	if len(m.outLines) != 2 || m.outLines[0] != "hello" || m.outLines[1] != "world" || m.outPartial != "" {
		t.Errorf("output lines = %q, partial %q", m.outLines, m.outPartial)
	}
	// A push for output already fetched does not fetch again.
	if _, cmd := m.Update(rpcclient.DebugChangedMsg{StateSeq: 1, OutputSeq: 3}); cmd != nil {
		if msgs := cmd(); msgs != nil {
			t.Errorf("an already-seen output seq triggered a fetch: %T", msgs)
		}
	}
}

func TestControlKeys(t *testing.T) {
	f := newFake()
	m := started(t, f)
	key(m, "c", "n", "i", "o", "x")
	want := []rpcclient.DebugAction{rpcclient.DebugContinue, rpcclient.DebugNext, rpcclient.DebugStepIn, rpcclient.DebugStepOut, rpcclient.DebugStop}
	if fmt.Sprint(f.controls) != fmt.Sprint(want) {
		t.Errorf("controls = %v, want %v", f.controls, want)
	}
}

// R restarts, and a failed restart says which configuration failed.
func TestRestartKey(t *testing.T) {
	f := newFake()
	m := key(started(t, f), "R")
	if f.restarts != 1 {
		t.Errorf("restarts = %d, want 1", f.restarts)
	}
	if !strings.Contains(m.status, "restart server: build failed") {
		t.Errorf("status = %q", m.status)
	}
}

// When the program resumes there is no stack; watches keep their expressions
// but lose values that belonged to the last stop.
func TestResumingClearsTheStop(t *testing.T) {
	f := newFake()
	m := started(t, f)
	m = key(m, "a", "x", " ", "+", " ", "1", "enter")
	f.mu.Lock()
	f.state = rpcclient.DebugState{Status: rpcclient.DebugRunning, Seq: 2}
	f.mu.Unlock()
	updated, cmd := m.Update(rpcclient.DebugChangedMsg{StateSeq: 2})
	m = drive(updated.(Model), cmd)
	if len(m.frames) != 0 || len(m.roots) != 0 {
		t.Error("stack or variables kept after the program resumed")
	}
	if len(m.watches) != 1 || m.watches[0].result != "" {
		t.Errorf("watches = %+v, want the expression kept and its value cleared", m.watches)
	}
	if !strings.Contains(screen(m), "running") {
		t.Errorf("header does not say running:\n%s", screen(m))
	}
}

// Answers overtaken by a newer stop or frame choice are dropped.
func TestStaleAnswersAreDropped(t *testing.T) {
	m := started(t, newFake())
	before := screen(m)
	updated, _ := m.Update(stackMsg{stateSeq: 0, frames: []rpcclient.DebugFrame{{ID: 9, Name: "stale.frame"}}})
	m = updated.(Model)
	updated, _ = m.Update(childrenMsg{gen: m.treeGen - 1, path: "Locals", vars: []rpcclient.DebugVariable{{Name: "stale", Value: "1"}}})
	m = updated.(Model)
	if after := screen(m); after != before || strings.Contains(after, "stale") {
		t.Errorf("a stale answer changed the window:\n%s", after)
	}
}

// The window fits its terminal, however much it has to show.
func TestRenderFitsTheTerminal(t *testing.T) {
	f := newFake()
	for i := 0; i < 200; i++ {
		f.output = append(f.output, rpcclient.DebugOutputChunk{Seq: uint64(i + 1), Text: fmt.Sprintf("line %d\n", i)})
	}
	m := started(t, f)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 24})
	m = updated.(Model)
	lines := strings.Split(m.render(), "\n")
	if len(lines) > 24 {
		t.Errorf("rendered %d lines into a 24-row terminal", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 60 {
			t.Errorf("a line is %d wide in a 60-column terminal: %q", w, ansi.Strip(l))
		}
	}
	// Output follows the end.
	if !strings.Contains(screen(m), "line 199") {
		t.Error("output is not showing its latest line")
	}
}

func TestNoSessionHeader(t *testing.T) {
	f := newFake()
	f.state = rpcclient.DebugState{}
	m := started(t, f)
	if !strings.Contains(screen(m), "no debug session") {
		t.Errorf("header:\n%s", screen(m))
	}
}

// An evaluation of an older watch list, arriving after a newer one, must not
// overwrite it: the watch added in between would vanish.
func TestStaleWatchEvaluationIsDropped(t *testing.T) {
	f := newFake()
	m := started(t, f)
	addWatch := func(m Model, expr string) (Model, tea.Cmd) {
		m = key(m, "a")
		for _, r := range expr {
			updated, _ := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			m = updated.(Model)
		}
		updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		return updated.(Model), cmd
	}
	m, first := addWatch(m, "x")  // evaluates [x], held back
	m, second := addWatch(m, "y") // evaluates [x y]
	updated, _ := m.Update(second())
	m = updated.(Model)
	updated, _ = m.Update(first()) // the slower, older answer lands last
	m = updated.(Model)
	if len(m.watches) != 2 || m.watches[1].expr != "y" {
		t.Errorf("watches = %+v, want x and y: the stale evaluation overwrote the newer list", m.watches)
	}
}
