package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/indiejames/indigo/internal/client"
)

// The App's half of debugging: it keeps the server's debug state and
// breakpoints, gives each buffer the part concerning its file (a
// client.DebugView), and moves the cursor to where the program stopped.
//
// Updates arrive as client.DebugChangedMsg carrying sequence numbers, pushed by
// the server whenever state, output or breakpoints move. The App refetches
// only what advanced past what it has already seen — pushes to different
// windows run concurrently and can arrive out of order, and a lower number is
// old news.

// appDebug is the App's copy of the server's debug state.
type appDebug struct {
	state       client.DebugState
	breakpoints map[string]map[int]client.BreakpointMark // canonical path → line → breakpoint
	stateSeen   uint64
	bpSeen      uint64
	jumpedSeq   uint64 // the state seq of the last stop this window jumped to
	gen         uint64 // bumped on every change; buffers re-render when it moves
	canon       map[string]string
}

func newAppDebug() *appDebug {
	return &appDebug{breakpoints: map[string]map[int]client.BreakpointMark{}, canon: map[string]string{}}
}

// debugStateMsg is a fetched session state. activeClient is the window the
// server considers most recently active, which decides who jumps to a stop.
type debugStateMsg struct {
	state        client.DebugState
	activeClient uint64
	err          error
}

// debugBreakpointsMsg is a fetched breakpoint list.
type debugBreakpointsMsg struct {
	breakpoints []client.DebugBreakpoint
	seq         uint64
	err         error
}

func (a App) fetchDebugState() tea.Cmd {
	rpc := a.rpc
	if rpc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		st, err := rpc.DebugState(ctx)
		if err != nil {
			return debugStateMsg{err: err}
		}
		var active uint64
		if ac, err := rpc.GetActiveContext(ctx); err == nil && ac.Found {
			active = ac.ClientID
		}
		return debugStateMsg{state: st, activeClient: active}
	}
}

func (a App) fetchDebugBreakpoints() tea.Cmd {
	rpc := a.rpc
	if rpc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bps, seq, err := rpc.ListBreakpoints(ctx, "")
		return debugBreakpointsMsg{breakpoints: bps, seq: seq, err: err}
	}
}

// initDebug fetches the current state and breakpoints at startup: a session
// may already be running, or another window may have set breakpoints.
func (a App) initDebug() tea.Cmd {
	return tea.Batch(a.fetchDebugState(), a.fetchDebugBreakpoints())
}

// handleDebugChanged refetches whatever the server says has moved.
func (a App) handleDebugChanged(msg client.DebugChangedMsg) (App, tea.Cmd) {
	if a.debug == nil {
		a.debug = newAppDebug()
	}
	var cmds []tea.Cmd
	if msg.StateSeq > a.debug.stateSeen {
		cmds = append(cmds, a.fetchDebugState())
	}
	if msg.BreakpointsSeq > a.debug.bpSeen {
		cmds = append(cmds, a.fetchDebugBreakpoints())
	}
	return a, tea.Batch(cmds...)
}

func (a App) handleDebugState(msg debugStateMsg) (App, tea.Cmd) {
	if a.debug == nil {
		a.debug = newAppDebug()
	}
	if msg.err != nil || msg.state.Seq < a.debug.stateSeen {
		return a, nil // a failed fetch, or an older answer overtaken by a newer one
	}
	prev := a.debug.state
	a.debug.state = msg.state
	a.debug.stateSeen = msg.state.Seq
	a.debug.gen++

	var cmd tea.Cmd
	st := msg.state
	switch {
	case st.Status == client.DebugStopped && st.Path != "" && st.Seq > a.debug.jumpedSeq:
		// Only the window the user was last in follows the stop. Every
		// window watches the session; if all of them jumped, a second window
		// open on unrelated code would be yanked away from it.
		if a.rpc != nil && msg.activeClient == a.rpc.ClientID() {
			a.debug.jumpedSeq = st.Seq
			cmd = a.doOpenFileAt(st.Path, st.Line)
		}
	case st.Status == client.DebugTerminated && prev.Status != client.DebugTerminated && prev.Status != client.DebugInactive:
		switch {
		case st.Error != "":
			a.status = "E: debug session ended: " + st.Error
		case st.HasExitCode:
			a.status = fmt.Sprintf("Debug session ended (exit code %d)", st.ExitCode)
		default:
			a.status = "Debug session ended"
		}
	}
	return a, cmd
}

func (a App) handleDebugBreakpoints(msg debugBreakpointsMsg) App {
	if a.debug == nil {
		a.debug = newAppDebug()
	}
	if msg.err != nil || msg.seq < a.debug.bpSeen {
		return a
	}
	byPath := map[string]map[int]client.BreakpointMark{}
	for _, bp := range msg.breakpoints {
		if byPath[bp.Path] == nil {
			byPath[bp.Path] = map[int]client.BreakpointMark{}
		}
		byPath[bp.Path][bp.Line] = client.BreakpointMark{
			Verified: bp.Verified, Detail: bp.Detail, Condition: bp.Condition, LogMessage: bp.LogMessage,
		}
	}
	a.debug.breakpoints = byPath
	a.debug.bpSeen = msg.seq
	a.debug.gen++
	return a
}

// canonical resolves symlinks in a buffer's path, the spelling the server keys
// breakpoints by and the debugger reports stops in. Cached: this runs for the
// active buffer after every message. A path that cannot be resolved — a
// container path seen from the host, a file not yet saved — is used as is.
func (d *appDebug) canonical(p string) string {
	if p == "" {
		return ""
	}
	if c, ok := d.canon[p]; ok {
		return c
	}
	c := p
	if r, err := filepath.EvalSymlinks(p); err == nil {
		c = r
	}
	d.canon[p] = c
	return c
}

// viewFor is what the buffer at path shows of the debug session.
func (d *appDebug) viewFor(path string) client.DebugView {
	v := client.DebugView{Gen: d.gen, Status: d.state.Status, Reason: d.state.Reason}
	cp := d.canonical(path)
	if bps := d.breakpoints[cp]; len(bps) > 0 {
		v.Breakpoints = bps
	} else if bps := d.breakpoints[path]; len(bps) > 0 {
		v.Breakpoints = bps
	}
	if d.state.Status == client.DebugStopped && d.state.Path != "" &&
		(d.state.Path == cp || d.state.Path == path) {
		v.StopLine, v.HasStop = d.state.Line, true
	}
	return v
}

// ensureActiveDebugView gives the active buffer the current debug view when
// its copy is out of date. Run after every message, like ensureActiveSized, so
// every path that creates or replaces a buffer model gets it without having to
// remember to — and it costs one integer comparison when nothing changed.
func (a App) ensureActiveDebugView() App {
	if a.debug == nil || a.active < 0 || a.active >= len(a.buffers) {
		return a
	}
	m := a.buffers[a.active]
	if m.DebugViewGen() == a.debug.gen {
		return a
	}
	a.buffers[a.active] = m.WithDebugView(a.debug.viewFor(m.FilePath()))
	return a
}

// breakpointSetMsg reports a SetBreakpoint from the condition/logpoint prompt.
type breakpointSetMsg struct{ err error }

// handleBreakpointPrompt opens the text prompt for a breakpoint's condition or
// logpoint message; Enter sets the breakpoint. The server pushes the change
// back like any other, so nothing here updates the gutter directly.
func (a App) handleBreakpointPrompt(msg client.BreakpointPromptMsg) App {
	title := fmt.Sprintf("Breakpoint condition — line %d", msg.Line+1)
	placeholder := "stop when true, e.g. i > 10 && name == \"x\"; empty: always"
	if msg.Log {
		title = fmt.Sprintf("Logpoint — line %d", msg.Line+1)
		placeholder = "print instead of stopping; {expr} is evaluated, e.g. i = {i}"
	}
	rpc := a.rpc
	a.pluginInput = &appPluginInput{
		title:       title,
		placeholder: placeholder,
		text:        msg.Current,
		width:       a.width,
		height:      a.height,
		onConfirm: func(text string) tea.Cmd {
			if rpc == nil {
				return nil
			}
			text = strings.TrimSpace(text)
			cond, logMsg := text, msg.Keep
			if msg.Log {
				cond, logMsg = msg.Keep, text
			}
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return breakpointSetMsg{err: rpc.SetBreakpoint(ctx, msg.Path, msg.Line, cond, logMsg)}
			}
		},
	}
	return a
}
