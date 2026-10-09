// Package debugwin is the `indigo --debug` window: a separate client showing
// the debug session's call stack, variables, watch expressions and program
// output, in its own terminal pane beside the editor windows.
//
// indigo leaves layout to the terminal (or zellij) rather than managing panes
// itself, so a debugger's panels are a window of their own rather than
// something squeezed into the editor. It attaches to the same server as the
// editor windows and sees the same session; the editor windows keep the
// breakpoints, the stopped-line marker and hover evaluation.
//
// It depends on rpcclient, not internal/client: it has no buffers to render and
// no reason to pull in the editor or its syntax highlighting.
package debugwin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/rpcclient"
	"github.com/indiejames/indigo/internal/staleprompt"
)

// Backend is the part of the server connection the window uses —
// *rpcclient.RPC in production, a fake in tests.
type Backend interface {
	DebugState(ctx context.Context) (rpcclient.DebugState, error)
	DebugStackTrace(ctx context.Context, threadID int64) ([]rpcclient.DebugFrame, error)
	DebugScopes(ctx context.Context, frameID int64) ([]rpcclient.DebugScope, error)
	DebugVariables(ctx context.Context, ref int64) ([]rpcclient.DebugVariable, error)
	DebugEvaluate(ctx context.Context, expr string, frameID int64, evalContext string) (rpcclient.DebugVariable, error)
	DebugOutput(ctx context.Context, sinceSeq uint64) ([]rpcclient.DebugOutputChunk, uint64, bool, error)
	DebugControl(ctx context.Context, action rpcclient.DebugAction) error
	DebugRestart(ctx context.Context, fallback rpcclient.DebugConfig) (rpcclient.DebugConfig, error)
	RequestOpenFile(ctx context.Context, path string, line uint32) error
	// ServerStale is what the server said at connect time about its own
	// build; true shows the stale-server prompt over the first frame. Part of
	// the interface rather than a setter so a window cannot be built without
	// the check — openUntitled once silently skipped the stderr version.
	ServerStale() bool
}

// section is one of the window's panels.
type section int

const (
	secStack section = iota
	secVars
	secWatches
	secOutput
	numSections
)

var sectionTitles = [numSections]string{"Call Stack", "Variables", "Watch", "Output"}

// varNode is a row of the variables tree: a scope, or a value within one.
type varNode struct {
	name, value, typ string
	ref              int64  // non-zero: has children
	path             string // "Locals/cfg/Name": identity across stops, for keeping expansion
	depth            int
	children         []*varNode
	loaded           bool
}

type watch struct {
	expr   string
	result string
	err    string
}

// Model is the debug window.
type Model struct {
	be     Backend
	width  int
	height int

	state     rpcclient.DebugState
	stateSeen uint64

	frames   []rpcclient.DebugFrame
	frameIdx int // the frame whose variables and watches are shown

	roots    []*varNode
	expanded map[string]bool // by path; survives across stops, as other debuggers do
	treeGen  int             // bumped when the tree is rebuilt; stale fetches are dropped

	watches []watch
	// watchGen is bumped whenever the watch list changes or an evaluation
	// starts, so a slower evaluation of an older list cannot overwrite a
	// watch added or deleted while it was in flight — treeGen alone does not
	// move for that.
	watchGen int

	outLines     []string
	outPartial   string // the last chunk's unterminated line
	outSeen      uint64
	outTruncated bool
	outFollow    bool // stay at the bottom until the user scrolls up

	focus  section
	cursor [numSections]int

	adding  bool // typing a new watch expression
	input   string
	status  string
	quitNow bool

	// stale is non-nil while the "server is running an older build" prompt
	// is showing; see internal/staleprompt.
	stale *staleprompt.Prompt
}

// New returns the window, reading from be.
func New(be Backend) Model {
	m := Model{be: be, expanded: map[string]bool{}, outFollow: true}
	if be.ServerStale() {
		m.stale = &staleprompt.Prompt{}
	}
	return m
}

// ---- messages ----

type stateMsg struct {
	st  rpcclient.DebugState
	err error
}

type stackMsg struct {
	stateSeq uint64
	frames   []rpcclient.DebugFrame
	err      error
}

type scopesMsg struct {
	gen    int
	scopes []rpcclient.DebugScope
	err    error
}

type childrenMsg struct {
	gen  int
	path string
	vars []rpcclient.DebugVariable
	err  error
}

type watchesMsg struct {
	gen      int
	watchGen int
	results  []watch
}

type outputMsg struct {
	chunks    []rpcclient.DebugOutputChunk
	latest    uint64
	truncated bool
	err       error
}

type controlMsg struct {
	what string
	err  error
}

const rpcTimeout = 10 * time.Second

func (m Model) call(fn func(ctx context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		defer cancel()
		return fn(ctx)
	}
}

func (m Model) fetchState() tea.Cmd {
	be := m.be
	return m.call(func(ctx context.Context) tea.Msg {
		st, err := be.DebugState(ctx)
		return stateMsg{st: st, err: err}
	})
}

func (m Model) fetchStack(stateSeq uint64) tea.Cmd {
	be := m.be
	return m.call(func(ctx context.Context) tea.Msg {
		frames, err := be.DebugStackTrace(ctx, 0)
		return stackMsg{stateSeq: stateSeq, frames: frames, err: err}
	})
}

func (m Model) fetchScopes(frameID int64, gen int) tea.Cmd {
	be := m.be
	return m.call(func(ctx context.Context) tea.Msg {
		scopes, err := be.DebugScopes(ctx, frameID)
		return scopesMsg{gen: gen, scopes: scopes, err: err}
	})
}

func (m Model) fetchChildren(n *varNode, gen int) tea.Cmd {
	be, ref, path := m.be, n.ref, n.path
	return m.call(func(ctx context.Context) tea.Msg {
		vars, err := be.DebugVariables(ctx, ref)
		return childrenMsg{gen: gen, path: path, vars: vars, err: err}
	})
}

// evalWatches evaluates every watch in the selected frame. Callers bump
// m.watchGen first, so this evaluation supersedes any still in flight.
func (m Model) evalWatches(gen int) tea.Cmd {
	if len(m.watches) == 0 || m.state.Status != rpcclient.DebugStopped || m.frameIdx >= len(m.frames) {
		return nil
	}
	be, watchGen := m.be, m.watchGen
	frameID := m.frames[m.frameIdx].ID
	exprs := make([]string, len(m.watches))
	for i, w := range m.watches {
		exprs[i] = w.expr
	}
	return m.call(func(ctx context.Context) tea.Msg {
		out := make([]watch, len(exprs))
		for i, e := range exprs {
			v, err := be.DebugEvaluate(ctx, e, frameID, "watch")
			out[i] = watch{expr: e, result: v.Value}
			if err != nil {
				out[i].err = err.Error()
			}
		}
		return watchesMsg{gen: gen, watchGen: watchGen, results: out}
	})
}

func (m Model) fetchOutput() tea.Cmd {
	be, since := m.be, m.outSeen
	return m.call(func(ctx context.Context) tea.Msg {
		chunks, latest, truncated, err := be.DebugOutput(ctx, since)
		return outputMsg{chunks: chunks, latest: latest, truncated: truncated, err: err}
	})
}

func (m Model) control(action rpcclient.DebugAction, what string) tea.Cmd {
	be := m.be
	return m.call(func(ctx context.Context) tea.Msg {
		return controlMsg{what: what, err: be.DebugControl(ctx, action)}
	})
}

// restart runs the last configuration again. No fallback: this window has no
// file of its own to guess a package from, so with nothing started yet the
// server says so.
func (m Model) restart() tea.Cmd {
	be := m.be
	return func() tea.Msg {
		// Long: a launch builds the program.
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		started, err := be.DebugRestart(ctx, rpcclient.DebugConfig{})
		what := "restart"
		if started.Name != "" || started.Program != "" {
			what = "restart " + started.Describe()
		}
		return controlMsg{what: what, err: err}
	}
}

// ---- update ----

// Init fetches the session as it stands: the window may open mid-session.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchState(), m.fetchOutput())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case rpcclient.DebugChangedMsg:
		var cmds []tea.Cmd
		if msg.StateSeq > m.stateSeen {
			cmds = append(cmds, m.fetchState())
		}
		if msg.OutputSeq > m.outSeen {
			cmds = append(cmds, m.fetchOutput())
		}
		return m, tea.Batch(cmds...)

	case stateMsg:
		return m.onState(msg)
	case stackMsg:
		return m.onStack(msg)
	case scopesMsg:
		return m.onScopes(msg)
	case childrenMsg:
		return m.onChildren(msg)
	case watchesMsg:
		if msg.gen == m.treeGen && msg.watchGen == m.watchGen {
			m.watches = msg.results
		}
		return m, nil
	case outputMsg:
		return m.onOutput(msg), nil
	case controlMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("%s: %v", msg.what, msg.err)
		}
		return m, nil

	case tea.KeyPressMsg:
		if m.stale != nil {
			return m.onStaleKey(msg)
		}
		return m.onKey(msg)
	}
	return m, nil
}

// onStaleKey answers the stale-server prompt. Every key goes to it while it is
// up, so none reaches the panels behind it.
func (m Model) onStaleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p, outcome := m.stale.Key(msg.String())
	m.stale = &p
	switch outcome {
	case staleprompt.Dismissed:
		m.stale = nil
	case staleprompt.QuitRequested:
		m.stale = nil
		m.quitNow = true
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) onState(msg stateMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil || msg.st.Seq < m.stateSeen {
		return m, nil
	}
	m.state, m.stateSeen = msg.st, msg.st.Seq
	if m.state.Status == rpcclient.DebugStopped {
		return m, m.fetchStack(m.state.Seq)
	}
	// Not stopped: there is no stack to show. Watches keep their expressions
	// and lose their values, which belonged to the last stop.
	m.frames, m.roots, m.frameIdx = nil, nil, 0
	m.treeGen++
	for i := range m.watches {
		m.watches[i].result, m.watches[i].err = "", ""
	}
	return m, nil
}

func (m Model) onStack(msg stackMsg) (tea.Model, tea.Cmd) {
	if msg.stateSeq != m.stateSeen {
		return m, nil // the session moved on while this was in flight
	}
	if msg.err != nil {
		m.status = "stack: " + msg.err.Error()
		return m, nil
	}
	m.frames = msg.frames
	m.frameIdx = 0
	m.cursor[secStack] = 0
	return m.selectFrame(0, false)
}

// selectFrame shows frame i's variables and watches, and — when the user chose
// it — moves the editor windows to it.
func (m Model) selectFrame(i int, reveal bool) (tea.Model, tea.Cmd) {
	if i < 0 || i >= len(m.frames) {
		return m, nil
	}
	m.frameIdx = i
	m.treeGen++
	m.roots = nil
	f := m.frames[i]
	m.watchGen++
	cmds := []tea.Cmd{m.fetchScopes(f.ID, m.treeGen), m.evalWatches(m.treeGen)}
	if reveal && f.Path != "" {
		be := m.be
		cmds = append(cmds, m.call(func(ctx context.Context) tea.Msg {
			be.RequestOpenFile(ctx, f.Path, uint32(max(f.Line, 0))) //nolint:errcheck
			return nil
		}))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onScopes(msg scopesMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.treeGen {
		return m, nil
	}
	if msg.err != nil {
		m.status = "scopes: " + msg.err.Error()
		return m, nil
	}
	m.roots = nil
	var cmds []tea.Cmd
	for i, sc := range msg.scopes {
		n := &varNode{name: sc.Name, ref: sc.Ref, path: sc.Name}
		m.roots = append(m.roots, n)
		// The first scope (locals) opens by default; others open only if the
		// user opened them at an earlier stop. Expensive scopes (globals)
		// never open by themselves.
		open, seen := m.expanded[n.path]
		if !seen {
			open = i == 0 && !sc.Expensive
			if open {
				m.expanded[n.path] = true
			}
		}
		if open && n.ref != 0 {
			cmds = append(cmds, m.fetchChildren(n, m.treeGen))
		}
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onChildren(msg childrenMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.treeGen {
		return m, nil
	}
	n := findNode(m.roots, msg.path)
	if n == nil {
		return m, nil
	}
	if msg.err != nil {
		n.children = []*varNode{{name: "error", value: msg.err.Error(), path: n.path + "/!", depth: n.depth + 1}}
		n.loaded = true
		return m, nil
	}
	n.children = nil
	var cmds []tea.Cmd
	for _, v := range msg.vars {
		c := &varNode{name: v.Name, value: v.Value, typ: v.Type, ref: v.Ref, path: n.path + "/" + v.Name, depth: n.depth + 1}
		n.children = append(n.children, c)
		// Re-open what the user had open at the last stop.
		if c.ref != 0 && m.expanded[c.path] {
			cmds = append(cmds, m.fetchChildren(c, m.treeGen))
		}
	}
	n.loaded = true
	return m, tea.Batch(cmds...)
}

func (m Model) onOutput(msg outputMsg) Model {
	if msg.err != nil || msg.latest < m.outSeen {
		return m
	}
	if msg.truncated {
		m.outTruncated = true
	}
	for _, c := range msg.chunks {
		if c.Seq <= m.outSeen {
			continue
		}
		text := m.outPartial + c.Text
		lines := strings.Split(text, "\n")
		m.outPartial = lines[len(lines)-1]
		m.outLines = append(m.outLines, lines[:len(lines)-1]...)
		m.outSeen = c.Seq
	}
	m.outSeen = max(m.outSeen, msg.latest)
	// Bounded like the server's copy: this window can stay open for days.
	if over := len(m.outLines) - maxOutLines; over > 0 {
		m.outLines = m.outLines[over:]
	}
	return m
}

const maxOutLines = 5000

func findNode(nodes []*varNode, path string) *varNode {
	for _, n := range nodes {
		if n.path == path {
			return n
		}
		if strings.HasPrefix(path, n.path+"/") {
			if f := findNode(n.children, path); f != nil {
				return f
			}
		}
	}
	return nil
}

// visibleVars is the variables tree flattened to the rows shown.
func (m Model) visibleVars() []*varNode {
	var out []*varNode
	var walk func([]*varNode)
	walk = func(ns []*varNode) {
		for _, n := range ns {
			out = append(out, n)
			if m.expanded[n.path] && n.loaded {
				walk(n.children)
			}
		}
	}
	walk(m.roots)
	return out
}

func (m Model) rowCount(s section) int {
	switch s {
	case secStack:
		return len(m.frames)
	case secVars:
		return len(m.visibleVars())
	case secWatches:
		return len(m.watches)
	case secOutput:
		n := len(m.outLines)
		if m.outPartial != "" {
			n++
		}
		return n
	}
	return 0
}

// ---- keys ----

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.adding {
		switch key {
		case "esc":
			m.adding, m.input = false, ""
		case "enter":
			if expr := strings.TrimSpace(m.input); expr != "" {
				m.watches = append(m.watches, watch{expr: expr})
			}
			m.adding, m.input = false, ""
			m.watchGen++
			return m, m.evalWatches(m.treeGen)
		case "backspace":
			if r := []rune(m.input); len(r) > 0 {
				m.input = string(r[:len(r)-1])
			}
		default:
			if t := msg.Key().Text; t != "" {
				m.input += t
			}
		}
		return m, nil
	}

	m.status = ""
	switch key {
	case "q", "ctrl+c":
		m.quitNow = true
		return m, tea.Quit
	case "tab":
		m.focus = (m.focus + 1) % numSections
	case "shift+tab":
		m.focus = (m.focus + numSections - 1) % numSections
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		m.move(-10)
	case "pgdown":
		m.move(10)

	// Execution: the same keys as the editor windows, plus single letters.
	case "c", "f5":
		return m, m.control(rpcclient.DebugContinue, "continue")
	case "n", "f10":
		return m, m.control(rpcclient.DebugNext, "step over")
	case "i", "f11":
		return m, m.control(rpcclient.DebugStepIn, "step in")
	case "o", "shift+f11":
		return m, m.control(rpcclient.DebugStepOut, "step out")
	case "p":
		return m, m.control(rpcclient.DebugPause, "pause")
	case "x", "shift+f5":
		return m, m.control(rpcclient.DebugStop, "stop")
	case "R", "ctrl+shift+f5":
		return m, m.restart()

	case "enter", "right", "l":
		return m.activate(key != "enter")
	case "left", "h":
		if m.focus == secVars {
			m.collapse()
		}
	case "a":
		m.adding, m.input = true, ""
		m.focus = secWatches
	case "d", "delete":
		if m.focus == secWatches && m.cursor[secWatches] < len(m.watches) {
			i := m.cursor[secWatches]
			m.watches = append(m.watches[:i], m.watches[i+1:]...)
			m.watchGen++ // an evaluation in flight still has the deleted one
			m.clampCursor(secWatches)
		}
	case "G":
		if m.focus == secOutput {
			m.outFollow = true
		}
	}
	return m, nil
}

func (m *Model) move(d int) {
	s := m.focus
	m.cursor[s] += d
	m.clampCursor(s)
	if s == secOutput {
		// Scrolling the output up stops it following new lines; reaching the
		// bottom again resumes.
		m.outFollow = m.cursor[s] >= m.rowCount(s)-1
	}
}

func (m *Model) clampCursor(s section) {
	n := m.rowCount(s)
	m.cursor[s] = max(0, min(m.cursor[s], n-1))
}

// activate is Enter (or →) on the focused row: choose a frame, or open a
// variable. → only ever opens; Enter toggles.
func (m Model) activate(openOnly bool) (tea.Model, tea.Cmd) {
	switch m.focus {
	case secStack:
		if !openOnly {
			return m.selectFrame(m.cursor[secStack], true)
		}
	case secVars:
		rows := m.visibleVars()
		i := m.cursor[secVars]
		if i >= len(rows) || rows[i].ref == 0 {
			return m, nil
		}
		n := rows[i]
		if m.expanded[n.path] && !openOnly {
			m.expanded[n.path] = false // remembered closed, so a later stop keeps it shut
			return m, nil
		}
		m.expanded[n.path] = true
		if !n.loaded {
			return m, m.fetchChildren(n, m.treeGen)
		}
	}
	return m, nil
}

// collapse closes the variable under the cursor, or moves to its parent.
func (m *Model) collapse() {
	rows := m.visibleVars()
	i := m.cursor[secVars]
	if i >= len(rows) {
		return
	}
	n := rows[i]
	if m.expanded[n.path] {
		m.expanded[n.path] = false
		return
	}
	if parent := n.path[:max(0, strings.LastIndex(n.path, "/"))]; parent != "" {
		for j, r := range rows {
			if r.path == parent {
				m.cursor[secVars] = j
				return
			}
		}
	}
}

// ---- view ----

var (
	titleStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#6699AA"))
	titleFocusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#88CCEE")).Bold(true)
	selStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#2D5F8A")).Foreground(lipgloss.Color("#FFFFFF"))
	dimStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#778899"))
	errStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#E05252"))
	stopStyle       = lipgloss.NewStyle().Background(lipgloss.Color("#9A7A10")).Foreground(lipgloss.Color("#000000")).Bold(true)
	runStyle        = lipgloss.NewStyle().Background(lipgloss.Color("#8A3A3A")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	idleStyle       = lipgloss.NewStyle().Background(lipgloss.Color("#3A4A5A")).Foreground(lipgloss.Color("#FFFFFF"))
	frameMarkStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCC00")).Bold(true)
)

// View draws the window.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return "indigo debug"
	}
	if m.stale != nil {
		// In place of the panels rather than over them: it is up only at
		// startup, before they hold anything worth seeing around it.
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.stale.Render(m.width))
	}
	lines := []string{m.header()}
	heights := m.sectionHeights(m.height - 2) // header and footer
	for s := section(0); s < numSections; s++ {
		lines = append(lines, m.renderSection(s, heights[s])...)
	}
	lines = append(lines, m.footer())
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m Model) header() string {
	st := m.state
	switch st.Status {
	case rpcclient.DebugStopped:
		where := ""
		if st.Path != "" {
			where = fmt.Sprintf(" at %s:%d", filepath.Base(st.Path), st.Line+1)
		}
		reason := st.Reason
		if reason == "" {
			reason = "stopped"
		}
		return stopStyle.Render(" "+reason+" ") + where
	case rpcclient.DebugRunning:
		return runStyle.Render(" running ")
	case rpcclient.DebugStarting:
		return runStyle.Render(" starting… ")
	case rpcclient.DebugTerminated:
		text := " ended "
		if st.HasExitCode {
			text = fmt.Sprintf(" ended (exit %d) ", st.ExitCode)
		}
		out := idleStyle.Render(text)
		if st.Error != "" {
			out += " " + errStyle.Render(strings.SplitN(st.Error, "\n", 2)[0])
		}
		return out
	}
	return idleStyle.Render(" no debug session ") + dimStyle.Render("  start one from an editor window (F5)")
}

func (m Model) footer() string {
	if m.adding {
		return "watch: " + m.input + "█"
	}
	if m.status != "" {
		return errStyle.Render(m.status)
	}
	return dimStyle.Render("tab section  ↑↓ move  enter select/expand  c continue  n over  i in  o out  x stop  R restart  a watch  q quit")
}

// sectionHeights splits the rows between the sections, each with one title row.
// Output gets what is left: it is the one that grows without limit.
func (m Model) sectionHeights(avail int) [numSections]int {
	var h [numSections]int
	h[secStack] = max(3, avail*20/100)
	h[secVars] = max(4, avail*35/100)
	h[secWatches] = max(2, min(len(m.watches)+2, avail*15/100))
	h[secOutput] = max(2, avail-h[secStack]-h[secVars]-h[secWatches])
	return h
}

func (m Model) renderSection(s section, height int) []string {
	title := "── " + sectionTitles[s] + " "
	style := titleStyle
	if s == m.focus {
		style = titleFocusStyle
	}
	if s == secOutput && m.outTruncated {
		title += "(earlier output dropped) "
	}
	out := []string{style.Render(title + strings.Repeat("─", max(0, m.width-len([]rune(title)))))}
	body := height - 1
	if body <= 0 {
		return out
	}
	rows := m.sectionRows(s)
	cur := m.cursor[s]
	start := 0
	switch {
	case s == secOutput && m.outFollow:
		start = max(0, len(rows)-body)
	default:
		start = max(0, min(cur-body/2, len(rows)-body))
	}
	for i := start; i < start+body; i++ {
		if i >= len(rows) {
			out = append(out, "")
			continue
		}
		row := rows[i]
		// A following output section has no cursor row to highlight.
		if s == m.focus && i == cur && (s != secOutput || !m.outFollow) {
			row = selStyle.Render(ansi.Strip(row) + strings.Repeat(" ", max(0, m.width-ansi.StringWidth(row))))
		}
		out = append(out, row)
	}
	return out
}

func (m Model) sectionRows(s section) []string {
	switch s {
	case secStack:
		rows := make([]string, len(m.frames))
		for i, f := range m.frames {
			mark := "  "
			if i == m.frameIdx {
				mark = frameMarkStyle.Render("▶ ")
			}
			loc := ""
			if f.Path != "" {
				loc = dimStyle.Render(fmt.Sprintf("  %s:%d", filepath.Base(f.Path), f.Line+1))
			}
			rows[i] = mark + f.Name + loc
		}
		if len(rows) == 0 && m.state.Status != rpcclient.DebugStopped {
			return []string{dimStyle.Render("  (not stopped)")}
		}
		return rows
	case secVars:
		var rows []string
		for _, n := range m.visibleVars() {
			marker := "  "
			if n.ref != 0 {
				marker = "▸ "
				if m.expanded[n.path] {
					marker = "▾ "
				}
			}
			line := strings.Repeat("  ", n.depth) + marker + n.name
			if n.depth > 0 {
				line += " = " + n.value
				if n.typ != "" {
					line += dimStyle.Render("  " + n.typ)
				}
			}
			rows = append(rows, line)
		}
		return rows
	case secWatches:
		rows := make([]string, len(m.watches))
		for i, w := range m.watches {
			switch {
			case w.err != "":
				rows[i] = w.expr + " " + errStyle.Render(w.err)
			case w.result != "":
				rows[i] = w.expr + " = " + w.result
			default:
				rows[i] = w.expr + dimStyle.Render("  (not evaluated)")
			}
		}
		if len(rows) == 0 {
			return []string{dimStyle.Render("  a: add a watch expression")}
		}
		return rows
	case secOutput:
		rows := append([]string(nil), m.outLines...)
		if m.outPartial != "" {
			rows = append(rows, m.outPartial)
		}
		return rows
	}
	return nil
}
