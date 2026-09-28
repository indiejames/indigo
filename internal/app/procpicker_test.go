package app

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
)

var testProcs = []client.DebugProcess{
	{PID: 4312, Name: "server", GoVersion: "go1.26.1", GoModule: "github.com/you/server", Args: []string{"--port", "8080"}},
	{PID: 4313, Name: "server", GoVersion: "go1.26.1", GoModule: "github.com/you/server", Args: []string{"--port", "9090"}},
	{PID: 5000, Name: "worker", GoVersion: "go1.25.0", GoModule: "github.com/you/worker"},
	{PID: 700, Name: "python3", Args: []string{"app.py"}},
	{PID: 800, Name: "node", Args: []string{"src/server.ts"}},
}

func procPickerApp(cfg client.DebugConfig, typed bool) App {
	a := App{cfg: nil, width: 120, height: 40}
	a, _ = a.openProcPicker(cfg, typed)
	return a.handleProcList(procListMsg{seq: a.procPicker.seq, procs: testProcs})
}

func typeInto(a App, s string) App {
	for _, r := range s {
		updated, _ := a.handleProcPickerKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		a = updated.(App)
	}
	return a
}

func labels(p *procPicker) []string {
	var out []string
	for _, r := range p.rows() {
		out = append(out, ansi.Strip(procLabel(r, 200)))
	}
	return out
}

// For Delve only Go programs are listed until Tab shows the rest; every word
// typed must match somewhere — pid, name, module or arguments — so two copies
// of one program are told apart by their flags.
func TestProcPickerFilters(t *testing.T) {
	a := procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true)
	if got := labels(a.procPicker); len(got) != 4 {
		t.Fatalf("Space d a lists Go and Node programs: %q", got)
	}
	a = typeInto(a, "server 9090")
	got := labels(a.procPicker)
	if len(got) != 1 || !strings.Contains(got[0], "4313") || !strings.Contains(got[0], "github.com/you/server (go1.26.1)") {
		t.Errorf("filtered: %q", got)
	}
	updated, _ := a.handleProcPickerKey(tea.KeyPressMsg{Code: tea.KeyTab})
	a = updated.(App)
	a.procPicker.query = "python"
	if got := labels(a.procPicker); len(got) != 1 || !strings.Contains(got[0], "python3") {
		t.Errorf("after Tab: %q, want the non-Go process too", got)
	}
}

// Space d a also takes something typed: host:port connects to a Delve, and a
// pid that is not listed can still be attached to.
func TestProcPickerTypedTargets(t *testing.T) {
	a := typeInto(procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true), "devbox:2345")
	cfg, ok := a.procPicker.choice()
	if !ok || cfg.Connect != "devbox:2345" || cfg.Request != "attach" {
		t.Errorf("host:port choice = %+v", cfg)
	}
	a = typeInto(procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true), "99999")
	if cfg, ok := a.procPicker.choice(); !ok || cfg.ProcessID != 99999 {
		t.Errorf("unlisted pid choice = %+v", cfg)
	}
	// A listed pid is just the listed row, not a duplicate typed one.
	a = typeInto(procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true), "5000")
	if got := labels(a.procPicker); len(got) != 1 || !strings.Contains(got[0], "worker") {
		t.Errorf("listed pid: %q", got)
	}
}

// Picking for a configuration keeps everything it says and fills in the
// process: a named Python attach stays Python, with its own settings.
func TestProcPickerCompletesAConfiguration(t *testing.T) {
	cfg := client.DebugConfig{Name: "py attach", Adapter: "python", Request: "attach", PickProcess: true,
		Launch: map[string]any{"justMyCode": false}}
	a := procPickerApp(cfg, false)
	if len(a.procPicker.rows()) != 5 {
		t.Errorf("an adapter indigo cannot judge should start with every process listed")
	}
	a = typeInto(a, "app.py")
	got, ok := a.procPicker.choice()
	if !ok || got.ProcessID != 700 || got.PickProcess || got.Name != "py attach" || got.Adapter != "python" || got.Launch["justMyCode"] != false {
		t.Errorf("choice = %+v", got)
	}
	// Typed host:port is not offered for a configuration: it names a process.
	a = typeInto(procPickerApp(cfg, false), "devbox:2345")
	if _, ok := a.procPicker.choice(); ok {
		t.Error("a configuration's picker offered a typed target")
	}
}

// A slow list for a picker that has since been closed and reopened is not
// shown in the new one.
func TestProcPickerDropsAStaleList(t *testing.T) {
	a := App{width: 120, height: 40}
	a, _ = a.openProcPicker(client.DebugConfig{Adapter: "go"}, true)
	first := a.procPicker.seq
	a, _ = a.openProcPicker(client.DebugConfig{Adapter: "go"}, true)
	a = a.handleProcList(procListMsg{seq: first, procs: testProcs})
	if !a.procPicker.loading || len(a.procPicker.procs) != 0 {
		t.Error("the first picker's list landed in the second")
	}
}

// Esc closes; the rendered box fits the window and says what it lists.
func TestProcPickerRenderAndClose(t *testing.T) {
	a := procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true)
	box := ansi.Strip(a.procPicker.render())
	for _, want := range []string{"Attach to a process", "Go and Node programs · Tab: all processes", "4312  server"} {
		if !strings.Contains(box, want) {
			t.Errorf("render lacks %q:\n%s", want, box)
		}
	}
	for _, line := range strings.Split(box, "\n") {
		if w := ansi.StringWidth(line); w > a.width {
			t.Fatalf("line %d wide in a %d window", w, a.width)
		}
	}
	updated, _ := a.handleProcPickerKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(App).procPicker != nil {
		t.Error("Esc did not close the picker")
	}
}

// Space d a on a Node program attaches with js-debug, not Delve; a Node
// configuration's picker lists only Node programs, a Go one's only Go.
func TestProcPickerNode(t *testing.T) {
	a := typeInto(procPickerApp(client.DebugConfig{Adapter: "go", Request: "attach"}, true), "server.ts")
	cfg, ok := a.procPicker.choice()
	if !ok || cfg.Adapter != "node" || cfg.Request != "attach" || cfg.ProcessID != 800 {
		t.Errorf("Space d a on node = %+v", cfg)
	}
	node := procPickerApp(client.DebugConfig{Name: "n", Adapter: "node", Request: "attach", PickProcess: true}, false)
	if got := labels(node.procPicker); len(got) != 1 || !strings.Contains(got[0], "node") {
		t.Errorf("node configuration lists %q", got)
	}
	if box := ansi.Strip(node.procPicker.render()); !strings.Contains(box, "Node programs · Tab: all processes") {
		t.Errorf("hint:\n%s", box)
	}
	goCfg := procPickerApp(client.DebugConfig{Name: "g", Adapter: "go", Request: "attach", PickProcess: true}, false)
	if got := labels(goCfg.procPicker); len(got) != 3 {
		t.Errorf("go configuration lists %q", got)
	}
}

// An attach that works but will not bind breakpoints (a program under tsx)
// says so where the user is looking. With one file open there is no tab bar,
// which is where the App draws its status, so the message goes to the
// buffer's own status line — it used to be set and never drawn.
func TestAttachWarningReachesTheStatusBar(t *testing.T) {
	// The warning is long, so it is shown as a toast that wraps — and so
	// is found on screen with its line breaks folded into spaces.
	onScreen := func(a App) string {
		return strings.Join(strings.Fields(strings.NewReplacer("│", " ").Replace(ansi.Strip(a.View().Content))), " ")
	}
	a := debugApp(t, "/w/index.ts") // one buffer: no tab bar
	a.fileChangedIdx = -1           // as New leaves it; a bare App would show the file-changed prompt
	updated, _ := a.Update(attachResultMsg{what: "process 30544", warning: "this program runs its TypeScript through tsx"})
	a = updated.(App)
	if !strings.Contains(onScreen(a), "W: attached to process 30544, but this program runs its TypeScript through tsx") {
		t.Errorf("one buffer: the warning is not on screen:\n%s", onScreen(a))
	}
	// The same message again is shown again, not swallowed as a repeat.
	a.buffers[0] = a.buffers[0].WithStatus("")
	updated, _ = a.Update(attachResultMsg{what: "process 30544", warning: "this program runs its TypeScript through tsx"})
	if !strings.Contains(onScreen(updated.(App)), "W: attached to process 30544") {
		t.Error("a repeated message was not shown")
	}
	// With a tab bar, the App's own status stays there.
	two := debugApp(t, "/w/index.ts", "/w/other.ts")
	updated, _ = two.Update(attachResultMsg{what: "process 1", err: errors.New("no such process")})
	if s := updated.(App).status; s != "E: attach: no such process" {
		t.Errorf("two buffers: status = %q", s)
	}
}
