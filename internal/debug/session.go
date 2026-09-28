package debug

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/dap"
	"github.com/indiejames/indigo/internal/document"
)

// Status is where a debug session is.
type Status int

const (
	StatusInactive Status = iota // no session
	StatusStarting               // adapter started, program building or launching
	StatusRunning
	StatusStopped
	StatusTerminated // the program ended; State says how
)

func (s Status) String() string {
	return [...]string{"inactive", "starting", "running", "stopped", "terminated"}[s]
}

// State is a snapshot of the session. Path and Line locate the top frame of
// the stopped thread when Status is StatusStopped.
type State struct {
	Status      Status
	ThreadID    int
	Path        string
	Line        int // 0-based
	Reason      string
	Description string
	ExitCode    int
	HasExitCode bool
	Error       string // why the session failed to start or ended abnormally
}

// Config is what to debug. Adapter selects the debugger; v1 has "go" (Delve).
type Config struct {
	Name       string // a named configuration's name; "" for an ad hoc one
	Adapter    string
	Mode       string // Delve: "debug" (a main package), "test" (a package's tests)
	Program    string // the package directory (or file) to debug
	Args       []string
	Cwd        string
	BuildFlags string
	Env        []string // "KEY=value", added to the program's environment
	// Launch is merged over the launch request indigo builds, for
	// adapter-specific settings.
	Launch map[string]any

	// Request is "launch" (the default: start the program) or "attach" (to
	// one already running, which a stop then detaches from rather than
	// ends).
	Request string
	// Connect is the host:port of a debug adapter that is already running —
	// `dlv --headless`, typically on another machine or in a container — to
	// use instead of starting one.
	Connect string
	// ProcessID is the process to attach to, for an attach that is not a
	// Connect.
	ProcessID int
	// PickProcess says the user chooses the process when the configuration
	// is started (launch.json's ${command:pickProcess}); the editor asks, and
	// starts it with ProcessID filled in.
	PickProcess bool
	// EnvFile is a .env file read each time the session starts (envfile.go);
	// its variables join Env, and Env's own entries win a clash.
	EnvFile string
}

// attaching reports whether cfg attaches to a program rather than starting it.
func (cfg Config) attaching() bool { return cfg.Request == "attach" }

// Frame is one stack frame.
type Frame struct {
	ID   int
	Name string
	Path string
	Line int // 0-based
	Col  int // 0-based
}

// Scope and Variable mirror the DAP types; Ref fetches children.
type Scope struct {
	Name      string
	Ref       int
	Expensive bool
}

type Variable struct {
	Name  string
	Value string
	Type  string
	Ref   int
}

// OutputChunk is a piece of program or adapter output, numbered in arrival
// order so a window can ask for everything after the last one it has.
type OutputChunk struct {
	Seq      uint64
	Category string // "stdout", "stderr", "console"
	Text     string
}

// Action is a request to change the program's execution.
type Action int

const (
	ActionContinue Action = iota
	ActionNext
	ActionStepIn
	ActionStepOut
	ActionPause
	ActionStop
)

// Notifier is told when any of the three things a window displays has moved:
// session state, output, breakpoints. Windows refetch whatever advanced, which
// makes a late or duplicated notification harmless — the reason notifications
// carry sequence numbers instead of the data.
type Notifier func(stateSeq, outputSeq, breakpointsSeq uint64)

// Manager owns the workspace's breakpoints and its debug session. One session
// at a time in v1.
type Manager struct {
	Breakpoints *Breakpoints

	notify Notifier
	// startAdapter launches the debugger for a config. A field so tests can
	// substitute a scripted adapter.
	startAdapter func(ctx context.Context, cfg Config, handler func(dap.Event)) (*dap.Client, error)
	// launchTimeout bounds getting from Start to a running program: an
	// adapter start plus a build. Long, because a cold build of a large
	// module is slow; finite, because a launch that never returns is exactly
	// what macOS does when Developer Mode is off.
	launchTimeout time.Duration
	// attachTimeout bounds an attach, which has no build to wait for.
	attachTimeout time.Duration

	mu       sync.Mutex
	last     *Config // the most recently started configuration, for Restart
	adapters []config.DebugAdapter
	client   *dap.Client   // the root connection, which owns the adapter
	children []*dap.Client // sessions the adapter asked for (children.go)
	active   *dap.Client   // the child that last stopped
	attached bool          // the session attached: stopping detaches, leaving the program running
	// connected: the session connected to an adapter someone else started
	// (Config.Connect). Such an adapter outlives the session, so it — not a
	// disconnect — decides whether the program runs on; see detach.
	connected bool
	// attachedPID is the process an OS-level debugger (Delve, lldb) attached
	// to by id, 0 otherwise; see unstickAfterDetach.
	attachedPID int
	ending      sync.WaitGroup // sessions whose adapter is still being shut down
	caps        dap.Capabilities
	gen         int // bumped per session, so a finished session's events are ignored
	state       State
	stateSeq    uint64

	outMu   sync.Mutex
	out     []OutputChunk
	outSeq  uint64
	outSize int
}

// Output retention: enough to scroll back through a test run, bounded so a
// program that prints forever cannot grow the server without limit.
const (
	maxOutputBytes  = 1 << 20
	maxOutputChunks = 10000
)

// NewManager returns a Manager that calls notify on every change. notify may
// be nil.
func NewManager(notify Notifier) *Manager {
	if notify == nil {
		notify = func(uint64, uint64, uint64) {}
	}
	m := &Manager{
		Breakpoints:   NewBreakpoints(),
		notify:        notify,
		launchTimeout: 3 * time.Minute,
		attachTimeout: 30 * time.Second,
	}
	m.startAdapter = m.startDefaultAdapter
	return m
}

func (m *Manager) fire() {
	m.mu.Lock()
	s := m.stateSeq
	m.mu.Unlock()
	m.outMu.Lock()
	o := m.outSeq
	m.outMu.Unlock()
	m.notify(s, o, m.Breakpoints.Seq())
}

// setState replaces the state for session gen; a stale session's update is
// dropped.
func (m *Manager) setState(gen int, st State) bool {
	m.mu.Lock()
	if gen != m.gen {
		m.mu.Unlock()
		return false
	}
	m.state = st
	m.stateSeq++
	m.mu.Unlock()
	m.fire()
	return true
}

// State returns the current session state and its sequence number.
func (m *Manager) State() (State, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, m.stateSeq
}

// ToggleBreakpoint adds or removes a breakpoint and, during a session, sends
// the file's new set to the adapter.
func (m *Manager) ToggleBreakpoint(path string, line int) bool {
	set := m.Breakpoints.Toggle(path, line)
	m.syncBreakpoints(path)
	m.fire()
	return set
}

// SetBreakpoint sets a breakpoint at path:line with a condition and a log
// message (either may be empty), sending it to a running session.
func (m *Manager) SetBreakpoint(path string, line int, condition, logMessage string) {
	if m.Breakpoints.Set(path, line, condition, logMessage) {
		m.syncBreakpoints(path)
		m.fire()
	}
}

// ApplyEdit keeps path's breakpoints on their lines through op, re-sending
// them to the adapter during a session when any moved.
func (m *Manager) ApplyEdit(path string, op document.Op) {
	if m.Breakpoints.ApplyEdit(path, op) {
		m.syncBreakpoints(path)
		m.fire()
	}
}

// syncBreakpoints sends path's breakpoints to a running session, in the
// background: it is called from edit paths that must not wait on an adapter.
func (m *Manager) syncBreakpoints(path string) {
	m.mu.Lock()
	gen := m.gen
	m.mu.Unlock()
	conns := m.connections()
	if len(conns) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Every connection: each child session is a separate program. Root
		// first, so a child's answer — the one about the running code —
		// is the one recorded.
		var err error
		for _, c := range conns {
			if e := m.sendBreakpoints(ctx, c, path); e != nil && err == nil {
				err = e
			}
		}
		m.mu.Lock()
		current := gen == m.gen
		m.mu.Unlock()
		if !current {
			return
		}
		if err != nil {
			// Said in the session's console, where the debug window shows
			// it: a breakpoint the user set that silently was not applied is
			// worse than a visible failure.
			m.appendOutput("console", fmt.Sprintf("could not update breakpoints in %s: %v\n", path, err))
		}
		m.fire()
	}()
}

// sendBreakpoints replaces path's breakpoints in the session with the store's.
//
// A condition or log message the adapter does not support is not sent at all,
// and the breakpoint is marked unverified with the reason. Sending it without
// would be worse either way: a conditional breakpoint would stop every time,
// and a logpoint would stop the program instead of printing.
func (m *Manager) sendBreakpoints(ctx context.Context, c *dap.Client, path string) error {
	m.mu.Lock()
	caps := m.caps
	m.mu.Unlock()
	bps, _ := m.Breakpoints.List(path)
	sent := make([]int, len(bps))
	res := make([]resultLine, len(bps))
	var req []dap.SourceBreakpoint
	var reqIdx []int // index into bps of each request entry
	for i, bp := range bps {
		sent[i] = bp.Line
		switch {
		case bp.LogMessage != "" && !caps.SupportsLogPoints:
			res[i] = resultLine{line: -1, message: "this debugger does not support logpoints"}
			continue
		case bp.Condition != "" && !caps.SupportsConditionalBreakpoints:
			res[i] = resultLine{line: -1, message: "this debugger does not support conditional breakpoints"}
			continue
		}
		req = append(req, dap.SourceBreakpoint{Line: bp.Line + 1, Condition: bp.Condition, LogMessage: bp.LogMessage})
		reqIdx = append(reqIdx, i)
	}
	results, err := c.SetBreakpoints(ctx, path, req)
	if err != nil {
		return err
	}
	for j, r := range results {
		if j >= len(reqIdx) {
			break
		}
		rl := resultLine{id: r.ID, verified: r.Verified, line: r.Line - 1, message: readableBreakpointMessage(r.Message)}
		if r.Line == 0 {
			rl.line = -1
		}
		res[reqIdx[j]] = rl
	}
	m.Breakpoints.setResults(path, c, sent, res)
	return nil
}

// ErrSessionActive is returned by Start while a session is running.
var ErrSessionActive = errors.New("a debug session is already running; stop it first")

// Start launches cfg under the debugger and returns once the program is
// running (or stopped at a breakpoint). Every breakpoint is sent before the
// program starts.
func (m *Manager) Start(cfg Config) error {
	_, err := m.StartWithWarning(cfg)
	return err
}

// StartWithWarning is Start, also returning a warning about a session that
// started but will not fully work — "" when there is none. The warning is
// written to the session's console too.
func (m *Manager) StartWithWarning(cfg Config) (warning string, err error) {
	if cfg.PickProcess && cfg.ProcessID <= 0 {
		return "", errors.New("this configuration attaches to a process chosen when it starts; start it from an editor window")
	}
	cfg, err = m.chooseAdapter(cfg)
	if err != nil {
		return "", err
	}
	if cfg.attaching() && cfg.ProcessID > 0 && isJSDebug(m.adapter(cfg.Adapter)) {
		warning = tsxWarning(cfg.ProcessID)
	}
	// What is recorded for Restart is the configuration as asked for — by
	// process id — so a restart re-attaches the same way.
	asked := cfg
	// Before the tsx preload, which adds to NODE_OPTIONS — a NODE_OPTIONS the
	// .env file sets has to be in Env by then, or it would be replaced.
	if cfg, err = withEnvFile(cfg); err != nil {
		return "", err
	}
	if isJSDebug(m.adapter(cfg.Adapter)) {
		cfg = withTSXPreload(cfg)
	}
	if cfg.attaching() && cfg.ProcessID > 0 && isJSDebug(m.adapter(cfg.Adapter)) {
		if _, hasPort := cfg.Launch["port"]; !hasPort {
			if cfg, err = attachNodeByPID(cfg); err != nil {
				return "", err
			}
		}
	}
	m.mu.Lock()
	// Starting counts as running: the adapter is not recorded until it is
	// up, and a second Start in that window would launch a second debugger.
	if m.client != nil || m.state.Status == StatusStarting {
		m.mu.Unlock()
		return "", ErrSessionActive
	}
	m.gen++
	gen := m.gen
	// Remembered even if the launch fails: the usual reason to restart is
	// to try again after fixing the build error it reported.
	last := asked
	m.last = &last
	m.attached = cfg.attaching()
	m.connected = cfg.Connect != ""
	m.attachedPID = 0
	if cfg.attaching() && cfg.ProcessID > 0 && !isJSDebug(m.adapter(cfg.Adapter)) {
		m.attachedPID = cfg.ProcessID
	}
	m.state = State{Status: StatusStarting}
	m.stateSeq++
	m.mu.Unlock()
	m.resetOutput()
	if warning != "" {
		m.appendOutput("console", "Warning: "+warning+"\n")
	}
	m.fire()

	// An attach builds nothing, so it gets far less than a launch: a wedged
	// adapter (Delve can wedge when attached to in the instant it is starting
	// its own program) should be reported in seconds, not after a build's
	// allowance.
	timeout := m.launchTimeout
	if cfg.attaching() {
		timeout = min(timeout, m.attachTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var client *dap.Client // set once the adapter is up; fail reads it
	fail := func(err error) error {
		// Let the output that arrived ahead of the failure be handled first:
		// it is the explanation (see dap.Client.WaitEventsIdle).
		if client != nil {
			wctx, wcancel := context.WithTimeout(context.Background(), 2*time.Second)
			client.WaitEventsIdle(wctx) //nolint:errcheck
			wcancel()
		}
		msg := err.Error()
		// Adapters report a launch failure as "check the debug console"
		// (Delve: "Build error: Check the debug console for details") and
		// put the substance — the compiler's errors — in console output that
		// has already arrived. Carry it in the error: the caller may have no
		// console to look at.
		if detail := m.consoleSince(0); detail != "" && !errors.Is(err, context.DeadlineExceeded) {
			msg += "\n" + detail
			err = errors.New(msg)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			msg = fmt.Sprintf("the debugger did not start the program within %v", timeout)
			if cfg.attaching() {
				msg = fmt.Sprintf("the debugger did not attach within %v", timeout)
			}
			if hint := PermissionHint(); hint != "" {
				msg += " — " + hint
			}
			err = errors.New(msg)
		}
		m.endSession(gen, State{Status: StatusTerminated, Error: msg})
		return err
	}

	handler := func(e dap.Event) { m.onEvent(gen, nil, e) }
	var c *dap.Client
	if cfg.Connect != "" {
		// An adapter someone else started: connect, and never kill it —
		// closing the connection is all indigo owns.
		c, err = dap.Dial(ctx, cfg.Connect, handler)
	} else {
		c, err = m.startAdapter(ctx, cfg, handler)
	}
	if err != nil {
		return "", fail(err)
	}
	c.SetReverseHandler(m.reverseHandler(gen, adapterID(cfg, m.adapter(cfg.Adapter))))
	client = c
	m.mu.Lock()
	if gen != m.gen {
		// Shutdown ran while the adapter was starting. Recording it now would
		// leave it running with nothing to stop it, and block every later
		// Start as "already running".
		m.mu.Unlock()
		c.Shutdown()
		return "", errors.New("the debug session was stopped while it was starting")
	}
	m.client = c
	m.mu.Unlock()
	go func() {
		// An adapter that dies ends the session, whether or not it said so.
		<-c.Done()
		m.mu.Lock()
		current := gen == m.gen && m.client == c
		st := m.state
		m.mu.Unlock()
		if current && st.Status != StatusTerminated {
			m.endSession(gen, State{Status: StatusTerminated, Error: "the debugger exited unexpectedly"})
		}
	}()

	caps, err := c.Initialize(ctx, adapterID(cfg, m.adapter(cfg.Adapter)))
	if err != nil {
		return "", fail(err)
	}
	m.mu.Lock()
	m.caps = caps
	m.mu.Unlock()

	args, err := launchArgs(cfg, m.adapter(cfg.Adapter))
	if err != nil {
		return "", fail(err)
	}
	request := "launch"
	if cfg.attaching() {
		request = "attach"
	}
	err = c.StartSession(ctx, caps, request, args, func(ctx context.Context) error {
		for _, path := range m.Breakpoints.Paths() {
			if err := m.sendBreakpoints(ctx, c, path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", fail(err)
	}
	// "stopped" may already have arrived (a breakpoint on the first line);
	// only move from starting to running.
	m.mu.Lock()
	st := m.state
	m.mu.Unlock()
	if st.Status == StatusStarting {
		m.setState(gen, State{Status: StatusRunning})
	}
	m.fire()
	return warning, nil
}

// Restart stops any running session and starts the most recently started
// configuration again — or fallback, when nothing has been started yet. It
// returns the configuration it launched.
func (m *Manager) Restart(fallback Config) (Config, error) {
	m.mu.Lock()
	cfg := fallback
	if m.last != nil {
		cfg = *m.last
	}
	active := m.client != nil
	gen := m.gen
	m.mu.Unlock()
	if cfg.Program == "" && cfg.Name == "" && !cfg.attaching() {
		return cfg, errors.New("nothing to restart: no debug session has been started yet")
	}
	if active {
		// Synchronous: endSession clears m.client before returning, so the
		// Start below does not see the old session as still running.
		m.endSession(gen, State{Status: StatusTerminated})
	}
	return cfg, m.Start(cfg)
}

// endSession closes the adapter and records how the session ended.
func (m *Manager) endSession(gen int, st State) {
	m.mu.Lock()
	if gen != m.gen {
		m.mu.Unlock()
		return
	}
	c := m.client
	children := m.children
	target := m.targetLocked()
	terminate := !m.attached
	resume := m.connected
	pid := m.attachedPID
	prev := m.state
	m.client, m.children, m.active = nil, nil, nil
	m.state = st
	m.stateSeq++
	m.mu.Unlock()
	if !terminate && c != nil {
		// Before anything is closed: the detach has to reach the child the
		// program stopped in, and the breakpoints every connection set.
		m.ending.Add(1)
		conns := append([]*dap.Client{c}, children...)
		go func() {
			defer m.ending.Done()
			m.detach(conns, target, prev, resume)
			for _, child := range children {
				child.Close() //nolint:errcheck
			}
			c.ShutdownWith(false)
			unstickAfterDetach(pid)
		}()
		m.Breakpoints.resetVerification()
		m.fire()
		return
	}
	for _, child := range children {
		go child.Close() //nolint:errcheck // the root's disconnect ends the program
	}
	if c != nil {
		// In the background — this is reached from event and RPC paths that
		// must not wait out an adapter's disconnect — but counted, so
		// Shutdown can wait for it: a server that exits mid-teardown
		// orphans the adapter process (found with js-debug, whose
		// disconnect is slow enough to lose that race).
		m.ending.Add(1)
		go func() {
			defer m.ending.Done()
			c.ShutdownWith(terminate)
		}()
	}
	m.Breakpoints.resetVerification()
	m.fire()
}

// detach leaves an attached program as if indigo had never been there: its
// breakpoints cleared and, if it is stopped, running again. The disconnect
// that follows cannot be relied on for either. A multi-client Delve server
// (`dlv --headless --accept-multiclient`) leaves the program exactly as the
// client left it — halted at the breakpoint that stopped it, with that
// breakpoint still set — and ignores DAP's suspendDebuggee (checked in Delve
// 1.26's source, onDisconnectRequest). A program paused on a remote machine
// with nobody attached is the worst outcome of a detach. Best effort, bounded:
// an adapter that has already gone fails these at once.
//
// resume is set only for a session that connected to an adapter someone else
// started. An adapter indigo started to attach by process id (dlv, js-debug)
// resumes the program itself when it detaches, and a continue sent just ahead
// of that detach races it: Delve halts the process to detach, and under load
// the halt could land around the still-settling continue and leave the
// program stopped — seen as process state T in TestAttachToARunningProcess,
// about one full test run in three.
func (m *Manager) detach(conns []*dap.Client, target *dap.Client, prev State, resume bool) {
	// Clear first — resumed with them still set, the program can stop on one
	// again at once, and a headless Delve would leave it there — then resume.
	// Each step has its own bound, so a slow first step cannot starve the
	// resume, the one whose omission leaves the program frozen.
	clearCtx, cancelClear := context.WithTimeout(context.Background(), 2*time.Second)
	for _, c := range conns {
		for _, path := range m.Breakpoints.Paths() {
			c.SetBreakpoints(clearCtx, path, nil) //nolint:errcheck
		}
	}
	cancelClear()
	if !resume || prev.Status != StatusStopped || target == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	thread := prev.ThreadID
	if thread == 0 {
		if threads, err := target.Threads(ctx); err == nil && len(threads) > 0 {
			thread = threads[0].ID
		}
	}
	target.Continue(ctx, thread) //nolint:errcheck
}

// unstickAfterDetach makes sure a process an OS-level debugger attached to by
// id is not left stopped once it has detached. Delve detaching on macOS can
// leave the process stopped (state T) when the machine is busy — seen in about
// one full test run in three, with no request of indigo's in flight; most
// likely the stop the attach delivered landing after the detach. A detached
// program must run, so a stopped one is sent SIGCONT, and watched for a
// moment in case the stray stop arrives late. A running process is left
// alone: this only ever resumes one that is stopped.
func unstickAfterDetach(pid int) {
	if pid <= 0 {
		return
	}
	for i := 0; i < 10; i++ {
		if processStopped(pid) {
			continueProcess(pid) //nolint:errcheck // gone, or not ours: nothing to do
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// onEvent handles an event from the session's root connection (from nil) or
// from a child session (children.go).
func (m *Manager) onEvent(gen int, from *dap.Client, e dap.Event) {
	switch e.Event {
	case "stopped":
		var ev dap.StoppedEvent
		e.Decode(&ev) //nolint:errcheck
		st := State{Status: StatusStopped, ThreadID: ev.ThreadID, Reason: ev.Reason, Description: ev.Description}
		m.confirmHit(gen, from, ev.HitBreakpointIDs)
		c := m.currentClient(gen)
		if from != nil {
			// The stopped program is this child's; stack, variables and
			// stepping have to go to it from now on.
			m.mu.Lock()
			if gen == m.gen {
				m.active = from
			}
			m.mu.Unlock()
			c = from
		}
		if c != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if frames, err := c.StackTrace(ctx, ev.ThreadID, 1); err == nil && len(frames) > 0 {
				if frames[0].Source != nil {
					st.Path = frames[0].Source.Path
				}
				st.Line = frames[0].Line - 1
			}
			cancel()
		}
		m.setState(gen, st)
	case "continued":
		m.mu.Lock()
		stopped := gen == m.gen && m.state.Status == StatusStopped
		m.mu.Unlock()
		if stopped {
			m.setState(gen, State{Status: StatusRunning})
		}
	case "exited":
		var ev dap.ExitedEvent
		e.Decode(&ev) //nolint:errcheck
		m.mu.Lock()
		if gen == m.gen {
			m.state.ExitCode, m.state.HasExitCode = ev.ExitCode, true
		}
		m.mu.Unlock()
	case "terminated":
		if from != nil {
			m.removeChild(gen, from) // one process ended; the root says when all have
			return
		}
		m.mu.Lock()
		st := State{Status: StatusTerminated, ExitCode: m.state.ExitCode, HasExitCode: m.state.HasExitCode}
		m.mu.Unlock()
		m.endSession(gen, st)
	case "breakpoint":
		var ev dap.BreakpointEvent
		e.Decode(&ev) //nolint:errcheck
		if ev.Reason != "changed" || ev.Breakpoint.ID == 0 {
			return
		}
		m.mu.Lock()
		conn := from
		if conn == nil && gen == m.gen {
			conn = m.client
		}
		m.mu.Unlock()
		r := resultLine{id: ev.Breakpoint.ID, verified: ev.Breakpoint.Verified, line: ev.Breakpoint.Line - 1, message: readableBreakpointMessage(ev.Breakpoint.Message)}
		if ev.Breakpoint.Line == 0 {
			r.line = -1
		}
		if conn != nil && m.Breakpoints.updateByID(conn, r) {
			m.fire()
		}
	case "output":
		var ev dap.OutputEvent
		e.Decode(&ev) //nolint:errcheck
		if ev.Category == "telemetry" {
			return
		}
		m.mu.Lock()
		current := gen == m.gen
		m.mu.Unlock()
		if !current {
			// A finished session's adapter can still be talking (Delve's
			// "Detaching" arrives after it said terminated); it must not land
			// in the next session's output.
			return
		}
		m.appendOutput(ev.Category, ev.Output)
		if from == nil && (ev.Category == "console" || ev.Category == "stderr") {
			m.noteExitStatus(gen, ev.Output)
		}
	}
}

// confirmHit marks the breakpoints that stopped the program as set. A
// breakpoint that was hit is plainly set, whatever the adapter said when it
// was sent: js-debug answers "provisional" when attaching to a program whose
// scripts are already loaded, and never sends the "changed" event that would
// confirm it — so without this a breakpoint that works is drawn as one that
// could not be set.
func (m *Manager) confirmHit(gen int, from *dap.Client, ids []int) {
	if len(ids) == 0 {
		return
	}
	m.mu.Lock()
	conn := from
	if conn == nil && gen == m.gen {
		conn = m.client
	}
	m.mu.Unlock()
	if conn == nil {
		return
	}
	changed := false
	for _, id := range ids {
		if m.Breakpoints.updateByID(conn, resultLine{id: id, verified: true, line: -1}) {
			changed = true
		}
	}
	if changed {
		m.fire()
	}
}

// exitStatus matches an adapter's own report of the program's exit, for the
// adapters that send no "exited" event: Delve ("Process 7 has exited with
// status 0", console) and js-debug ("Process exited with code 3", stderr,
// on the root session — checked against js-debug 1.140).
var exitStatus = regexp.MustCompile(`(?:has exited with status|^Process exited with code) (-?\d+)`)

// noteExitStatus records the program's exit code from an adapter's console
// message, when no "exited" event has already given it.
//
// Delve sends no "exited" event at all — checked against 1.26.1: it reports the
// status only as "Process N has exited with status 0" on the console, after its
// first "terminated". js-debug does the same on stderr. Deliberately narrow
// (exitStatus names both messages exactly): a session with neither leaves
// HasExitCode false rather than claiming 0. Only the root session's output is
// read, never the program's own, which could print anything.
func (m *Manager) noteExitStatus(gen int, text string) {
	match := exitStatus.FindStringSubmatch(text)
	if match == nil {
		return
	}
	code, err := strconv.Atoi(match[1])
	if err != nil {
		return
	}
	m.mu.Lock()
	if gen != m.gen || m.state.HasExitCode {
		m.mu.Unlock()
		return
	}
	m.state.ExitCode, m.state.HasExitCode = code, true
	m.stateSeq++
	m.mu.Unlock()
	m.fire()
}

func (m *Manager) currentClient(gen int) *dap.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.gen {
		return nil
	}
	return m.targetLocked()
}

// ErrNoSession is returned by requests that need a running session.
var ErrNoSession = errors.New("no debug session is running")

func (m *Manager) session() (*dap.Client, State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client == nil {
		return nil, m.state, ErrNoSession
	}
	return m.targetLocked(), m.state, nil
}

// Control continues, steps, pauses or stops the program.
func (m *Manager) Control(a Action) error {
	c, st, err := m.session()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if a == ActionStop {
		m.mu.Lock()
		gen := m.gen
		m.mu.Unlock()
		m.endSession(gen, State{Status: StatusTerminated})
		return nil
	}
	thread := st.ThreadID
	if thread == 0 {
		if threads, err := c.Threads(ctx); err == nil && len(threads) > 0 {
			thread = threads[0].ID
		}
	}
	switch a {
	case ActionPause:
		return c.Pause(ctx, thread)
	}
	if st.Status != StatusStopped {
		return errors.New("the program is running; pause it first")
	}
	// The program is about to run. Say so now rather than waiting for a
	// "continued" event: Delve, like most adapters, does not send one for a
	// continue the client asked for.
	m.mu.Lock()
	gen := m.gen
	m.mu.Unlock()
	m.setState(gen, State{Status: StatusRunning})
	switch a {
	case ActionContinue:
		err = c.Continue(ctx, thread)
	case ActionNext:
		err = c.Next(ctx, thread)
	case ActionStepIn:
		err = c.StepIn(ctx, thread)
	case ActionStepOut:
		err = c.StepOut(ctx, thread)
	}
	return err
}

// StackTrace returns thread's stack (0: the stopped thread).
func (m *Manager) StackTrace(threadID int) ([]Frame, error) {
	c, st, err := m.session()
	if err != nil {
		return nil, err
	}
	if threadID == 0 {
		threadID = st.ThreadID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames, err := c.StackTrace(ctx, threadID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Frame, len(frames))
	for i, f := range frames {
		out[i] = Frame{ID: f.ID, Name: f.Name, Line: f.Line - 1, Col: max(f.Column-1, 0)}
		if f.Source != nil {
			out[i].Path = f.Source.Path
		}
	}
	return out, nil
}

// Scopes returns a frame's variable groups.
func (m *Manager) Scopes(frameID int) ([]Scope, error) {
	c, _, err := m.session()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	scopes, err := c.Scopes(ctx, frameID)
	if err != nil {
		return nil, err
	}
	out := make([]Scope, len(scopes))
	for i, s := range scopes {
		out[i] = Scope{Name: s.Name, Ref: s.VariablesReference, Expensive: s.Expensive}
	}
	return out, nil
}

// Variables returns the children of ref.
func (m *Manager) Variables(ref int) ([]Variable, error) {
	c, _, err := m.session()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vars, err := c.Variables(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]Variable, len(vars))
	for i, v := range vars {
		out[i] = Variable{Name: v.Name, Value: v.Value, Type: v.Type, Ref: v.VariablesReference}
	}
	return out, nil
}

// Evaluate evaluates expr in frameID (0: the top frame of the stopped thread).
//
// frameID 0 means the stopped thread's top frame, which is resolved to that
// frame's real id rather than sent as 0: some adapters number frames from 0
// (js-debug), so 0 is ambiguous on the wire, and sending no frame at all
// evaluates globally, where the stopped function's locals do not exist.
func (m *Manager) Evaluate(expr string, frameID int, evalContext string) (Variable, error) {
	c, st, err := m.session()
	if err != nil {
		return Variable{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var r dap.EvaluateResult
	switch {
	case frameID != 0:
		r, err = c.Evaluate(ctx, expr, frameID, evalContext)
	case st.Status == StatusStopped:
		frames, ferr := c.StackTrace(ctx, st.ThreadID, 1)
		if ferr != nil || len(frames) == 0 {
			r, err = c.EvaluateGlobal(ctx, expr, evalContext)
		} else {
			r, err = c.Evaluate(ctx, expr, frames[0].ID, evalContext)
		}
	default:
		r, err = c.EvaluateGlobal(ctx, expr, evalContext)
	}
	if err != nil {
		return Variable{}, err
	}
	return Variable{Name: expr, Value: r.Result, Type: r.Type, Ref: r.VariablesReference}, nil
}

func (m *Manager) resetOutput() {
	m.outMu.Lock()
	m.out, m.outSize = nil, 0
	m.outMu.Unlock()
}

func (m *Manager) appendOutput(category, text string) {
	m.outMu.Lock()
	m.outSeq++
	m.out = append(m.out, OutputChunk{Seq: m.outSeq, Category: category, Text: text})
	m.outSize += len(text)
	for len(m.out) > 1 && (m.outSize > maxOutputBytes || len(m.out) > maxOutputChunks) {
		m.outSize -= len(m.out[0].Text)
		m.out = m.out[1:]
	}
	m.outMu.Unlock()
	m.fire()
}

// consoleSince joins the console and stderr output after sinceSeq — what an
// adapter says about a failed launch — keeping the last few lines.
func (m *Manager) consoleSince(sinceSeq uint64) string {
	m.outMu.Lock()
	defer m.outMu.Unlock()
	var lines []string
	for _, c := range m.out {
		if c.Seq > sinceSeq && (c.Category == "console" || c.Category == "stderr") {
			for _, l := range strings.Split(strings.TrimRight(c.Text, "\n"), "\n") {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
		}
	}
	const keep = 15
	if len(lines) > keep {
		lines = append([]string{"…"}, lines[len(lines)-keep:]...)
	}
	return strings.Join(lines, "\n")
}

// Output returns every chunk after sinceSeq, the latest seq, and whether
// chunks after sinceSeq were dropped to stay within the retention bound.
func (m *Manager) Output(sinceSeq uint64) (chunks []OutputChunk, latest uint64, truncated bool) {
	m.outMu.Lock()
	defer m.outMu.Unlock()
	for _, c := range m.out {
		if c.Seq > sinceSeq {
			chunks = append(chunks, c)
		}
	}
	truncated = len(m.out) > 0 && m.out[0].Seq > sinceSeq+1
	return chunks, m.outSeq, truncated
}

// Shutdown ends any session; for server exit.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	c := m.client
	children := m.children
	target := m.targetLocked()
	terminate := !m.attached
	resume := m.connected
	pid := m.attachedPID
	prev := m.state
	m.client, m.children, m.active = nil, nil, nil
	m.gen++
	m.mu.Unlock()
	if c != nil && !terminate {
		m.detach(append([]*dap.Client{c}, children...), target, prev, resume)
	}
	for _, child := range children {
		child.Close() //nolint:errcheck
	}
	if c != nil {
		c.ShutdownWith(terminate)
		if !terminate {
			unstickAfterDetach(pid)
		}
	}
	m.ending.Wait() // sessions already ending, still disconnecting
}

// ---- adapters ----

// FindDelve locates dlv on PATH, then where `go install` puts it — a server
// started from a GUI terminal launcher often has neither GOBIN nor ~/go/bin on
// its PATH, and "dlv not found" when it is installed would be a poor answer.
func FindDelve() (string, error) {
	if p, err := exec.LookPath("dlv"); err == nil {
		return p, nil
	}
	var candidates []string
	if d := os.Getenv("GOBIN"); d != "" {
		candidates = append(candidates, filepath.Join(d, "dlv"))
	}
	if d := os.Getenv("GOPATH"); d != "" {
		for _, p := range filepath.SplitList(d) {
			candidates = append(candidates, filepath.Join(p, "bin", "dlv"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, "go", "bin", "dlv"))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", errors.New("the Delve debugger (dlv) was not found — install it with `go install github.com/go-delve/delve/cmd/dlv@latest`")
}

func resolve(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// PermissionHint explains why this machine may be unable to start a program
// under a debugger, or returns "". On macOS with Developer Mode off, every
// launch waits on a GUI authorization prompt that nothing headless can answer,
// and the launch never returns — found while building this, on the
// development machine.
func PermissionHint() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("DevToolsSecurity", "-status").CombinedOutput()
	if err == nil && strings.Contains(string(out), "disabled") {
		return "macOS Developer Mode is disabled, so the launch is waiting on an authorization " +
			"prompt; run `sudo DevToolsSecurity -enable`"
	}
	return ""
}

// readableBreakpointMessage turns an adapter's reason for an unset breakpoint
// into something to show. js-debug sends the same state two ways — the
// localization key, and elsewhere its English text "Unbound breakpoint" — and
// indigo shows the reason after the line.
func readableBreakpointMessage(msg string) string {
	switch msg {
	case "breakpoint.provisionalBreakpoint", "Unbound breakpoint":
		// Not "cannot be set": js-debug says this when attaching even for a
		// breakpoint that works, and confirms it only by stopping there.
		return "not confirmed yet — shown as set once the program stops here"
	}
	return msg
}
