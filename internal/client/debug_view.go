package client

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Debugging in the editor window: breakpoint markers, the stopped line, the
// status-bar badge, the keys, and hover evaluation. The session and the
// breakpoints belong to the server (internal/debug); the App fetches them and
// hands each buffer the part that concerns its file as a DebugView.

// DebugView is what a buffer shows of the debug session.
type DebugView struct {
	// Gen identifies the App's debug data this view was built from; the App
	// re-applies a view only when its own Gen has moved past it.
	Gen uint64
	// Breakpoints maps a 0-based line to its breakpoint.
	Breakpoints map[int]BreakpointMark
	// StopLine is the line the session is stopped on in this file, when
	// HasStop. A flag rather than a -1 sentinel: a Model that was never given
	// a view has the zero value, and StopLine 0 would draw the stopped-here
	// arrow on the first line of every file.
	StopLine int
	HasStop  bool
	// Status and Reason drive the status-bar badge.
	Status DebugStatus
	Reason string
}

// BreakpointMark is what a buffer knows of one breakpoint.
type BreakpointMark struct {
	Verified   bool   // the running session's debugger confirmed it
	Detail     string // why it could not, when not
	Condition  string // stop only when this is true
	LogMessage string // a logpoint: print this instead of stopping
}

// WithDebugView replaces what this buffer shows of the debug session.
func (m Model) WithDebugView(v DebugView) Model {
	m.debug = v
	return m
}

// DebugViewGen reports which App debug data this buffer is showing.
func (m Model) DebugViewGen() uint64 { return m.debug.Gen }

// sessionActive reports whether a debug session is under way.
func (v DebugView) sessionActive() bool {
	return v.Status == DebugStarting || v.Status == DebugRunning || v.Status == DebugStopped
}

var (
	debugStopMarkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCC00")).Bold(true)
	debugBPMarkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#E05252"))
	debugBadgeStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#8A3A3A")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	debugBadgeStopped  = lipgloss.NewStyle().Background(lipgloss.Color("#9A7A10")).Foreground(lipgloss.Color("#000000")).Bold(true)
	// debugStopLineBG tints the stopped line, the way other editors mark the
	// current execution point.
	debugStopLineBG = bgSGR("#3D3818")
)

// debugGutterMark is the left-gutter text for lineNum — the stopped-here arrow,
// or a breakpoint — or "" when the debugger has nothing to show there. The
// glyph says the kind: ● a breakpoint, ◉ a conditional one, ◆ a logpoint. One
// the running session could not set is hollow (○ ◎ ◇).
func (m Model) debugGutterMark(lineNum int) string {
	if m.debug.HasStop && m.debug.StopLine == lineNum {
		return debugStopMarkStyle.Render("▶ ")
	}
	bp, ok := m.debug.Breakpoints[lineNum]
	if !ok {
		return ""
	}
	hollow := m.debug.sessionActive() && !bp.Verified
	var solid, open string
	switch {
	case bp.LogMessage != "":
		solid, open = "◆", "◇"
	case bp.Condition != "":
		solid, open = "◉", "◎"
	default:
		solid, open = "●", "○"
	}
	if hollow {
		return debugBPMarkStyle.Render(open + " ")
	}
	return debugBPMarkStyle.Render(solid + " ")
}

// breakpointNote is the dim text shown after a line holding a conditional
// breakpoint or a logpoint — otherwise the condition is invisible — or ""
// when there is nothing to say. A breakpoint the session could not set says
// why, which is the only place that reason is shown.
func (m Model) breakpointNote(line int) string {
	bp, ok := m.debug.Breakpoints[line]
	if !ok {
		return ""
	}
	var note string
	switch {
	case bp.LogMessage != "":
		note = "log: " + bp.LogMessage
		if bp.Condition != "" {
			note += "  if " + bp.Condition
		}
	case bp.Condition != "":
		note = "if " + bp.Condition
	}
	if m.debug.sessionActive() && !bp.Verified && bp.Detail != "" {
		if note != "" {
			note += " — "
		}
		note += bp.Detail
	}
	return note
}

// buildBreakpointNoteOverlays places breakpointNote after the end of each
// visible line that has one, on the line's last screen row, cut to fit.
func (m Model) buildBreakpointNoteOverlays(layout []layoutEntry, cw int) [][]lineOverlay {
	if len(m.debug.Breakpoints) == 0 {
		return nil
	}
	var rows [][]lineOverlay
	for line := range m.debug.Breakpoints {
		note := m.breakpointNote(line)
		if note == "" || line < 0 || line >= m.buf.LineCount() {
			continue
		}
		exp, _ := expandTabsRemap([]rune(m.buf.Line(line)))
		end := len(exp)
		row := screenRowOf(layout, line, end, cw)
		if row < 0 {
			continue
		}
		col := end - layout[row].chunkStart
		room := cw - col - 3 // two spaces before the note, one to spare
		if room < 4 {
			continue
		}
		text := []rune(note)
		if len(text) > room {
			text = append(text[:room-1], '…')
		}
		if rows == nil {
			rows = make([][]lineOverlay, len(layout))
		}
		rows[row] = append(rows[row], lineOverlay{col: col, text: inlayHintStyle.Render("  " + string(text)), w: 0})
	}
	return rows
}

// hasDebugGutterContent reports whether the left gutter must be shown for the
// debugger even with line numbers off.
func (m Model) hasDebugGutterContent() bool {
	return len(m.debug.Breakpoints) > 0 || m.debug.HasStop
}

// debugStatusBadge is the status-bar segment for a session, or "".
func (m Model) debugStatusBadge() string {
	switch m.debug.Status {
	case DebugStarting:
		return debugBadgeStyle.Render(" DEBUG starting… ")
	case DebugRunning:
		return debugBadgeStyle.Render(" DEBUG running ")
	case DebugStopped:
		text := " DEBUG stopped "
		if m.debug.Reason != "" {
			text = " DEBUG " + m.debug.Reason + " "
		}
		return debugBadgeStopped.Render(text)
	}
	return ""
}

// ---- keys ----

// debugResultMsg reports the outcome of a debug command started from this
// buffer; a non-empty err is shown in the status bar.
type debugResultMsg struct {
	bufID uint32
	what  string
	err   error
}

func (m Model) debugCmd(what string, timeout time.Duration, run func(ctx context.Context) error) tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return debugResultMsg{bufID: bufID, what: what, err: run(ctx)}
	}
}

func executeToggleBreakpoint(m Model) (tea.Model, tea.Cmd) {
	if m.filePath == "" {
		return m.pushStatus("Save the file before setting a breakpoint"), nil
	}
	path, line, rpc := m.filePath, m.cursor.Line, m.rpc
	return m, m.debugCmd("toggle breakpoint", 5*time.Second, func(ctx context.Context) error {
		_, err := rpc.ToggleBreakpoint(ctx, path, line)
		return err
	})
}

// BreakpointPromptMsg asks the App to prompt for a breakpoint's condition, or
// with Log for a logpoint's message, and set the breakpoint with the answer.
// Current pre-fills the prompt; Keep is the other field, carried through
// unchanged so editing one does not clear the other.
type BreakpointPromptMsg struct {
	Path    string
	Line    int
	Log     bool
	Current string
	Keep    string
}

func breakpointPrompt(log bool) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		if m.filePath == "" {
			return m.pushStatus("Save the file before setting a breakpoint"), nil
		}
		bp := m.debug.Breakpoints[m.cursor.Line]
		msg := BreakpointPromptMsg{Path: m.filePath, Line: m.cursor.Line, Log: log, Current: bp.Condition, Keep: bp.LogMessage}
		if log {
			msg.Current, msg.Keep = bp.LogMessage, bp.Condition
		}
		return m, func() tea.Msg { return msg }
	}
}

var (
	executeBreakpointCondition = breakpointPrompt(false)
	executeLogpoint            = breakpointPrompt(true)
)

// debugConfigFor is the launch configuration for debugging this buffer: for
// Go, its package (as a test run in a test file); for anything else, the file
// itself, with the adapter left for the server to choose by extension from
// its [[debug_adapter]] list — which lives in the server's config, the one
// that matters inside a container.
func (m Model) debugConfigFor() (DebugConfig, error) {
	if m.filePath == "" {
		return DebugConfig{}, fmt.Errorf("save the file before debugging it")
	}
	dir := filepath.Dir(m.filePath)
	if !strings.HasSuffix(m.filePath, ".go") {
		return DebugConfig{Program: m.filePath, Cwd: dir}, nil
	}
	mode := "debug"
	if strings.HasSuffix(m.filePath, "_test.go") {
		mode = "test"
	}
	return DebugConfig{Adapter: "go", Mode: mode, Program: dir, Cwd: dir}, nil
}

// goTestFunc matches the first line of a top-level Go test, benchmark, fuzz
// test or example. Methods (a testify suite's) do not match: `go test -run`
// cannot select one.
var goTestFunc = regexp.MustCompile(`^func ((?:Test|Benchmark|Fuzz|Example)\w*)\(`)

// testAtCursor finds the Go test function the cursor is in, or "".
//
// A text scan rather than document symbols: gofmt puts a top-level func and
// its closing brace in column 0, which is all this needs, and it answers
// synchronously and without a language server. The nearest top-level func at
// or above the cursor is the enclosing one unless a column-0 "}" closes it
// first.
func (m Model) testAtCursor() string {
	if !strings.HasSuffix(m.filePath, "_test.go") {
		return ""
	}
	for l := m.cursor.Line; l >= 0; l-- {
		line := m.buf.Line(l)
		if strings.HasPrefix(line, "func ") {
			if sub := goTestFunc.FindStringSubmatch(line); sub != nil {
				return sub[1]
			}
			return ""
		}
		if l < m.cursor.Line && strings.HasPrefix(line, "}") {
			return "" // the cursor is below the end of the function above it
		}
	}
	return ""
}

// testArgs selects exactly one test for `go test`. A benchmark runs no tests
// alongside it; a fuzz test runs its seed corpus, which is what debugging it
// wants.
func testArgs(name string) []string {
	re := "^" + regexp.QuoteMeta(name) + "$"
	if strings.HasPrefix(name, "Benchmark") {
		return []string{"-test.run", "^$", "-test.bench", re}
	}
	return []string{"-test.run", re}
}

func executeDebugStart(m Model) (tea.Model, tea.Cmd) {
	cfg, err := m.debugConfigFor()
	if err != nil {
		return m.pushStatus("E: " + err.Error()), nil
	}
	return m.startDebugging(cfg)
}

// executeDebugTest debugs the test function the cursor is in.
func executeDebugTest(m Model) (tea.Model, tea.Cmd) {
	name := m.testAtCursor()
	if name == "" {
		return m.pushStatus("The cursor is not in a Go test function"), nil
	}
	cfg, err := m.debugConfigFor()
	if err != nil {
		return m.pushStatus("E: " + err.Error()), nil
	}
	cfg.Args = testArgs(name)
	return m.startDebugging(cfg)
}

func (m Model) startDebugging(cfg DebugConfig) (tea.Model, tea.Cmd) {
	rpc := m.rpc
	m = m.pushStatus("Starting debugger for " + cfg.Describe() + "…")
	// Long: a launch builds the program. The server has its own bound.
	return m, m.debugCmd("debug "+cfg.Describe(), 4*time.Minute, func(ctx context.Context) error {
		return rpc.DebugStart(ctx, cfg)
	})
}

// executeDebugRestart stops any session and runs the last configuration
// again — or this buffer's package, when nothing has been debugged yet.
func executeDebugRestart(m Model) (tea.Model, tea.Cmd) {
	if m.rpc == nil {
		return m, nil
	}
	fallback, _ := m.debugConfigFor() // none (a non-Go buffer) is fine while there is a last config
	rpc := m.rpc
	m = m.pushStatus("Starting debugger…")
	return m, m.debugCmd("restart debugging", 4*time.Minute, func(ctx context.Context) error {
		started, err := rpc.DebugRestart(ctx, fallback)
		if err != nil && (started.Program != "" || started.Name != "") {
			return fmt.Errorf("%s: %w", started.Describe(), err)
		}
		return err
	})
}

// ---- named configurations ----

// debugConfigsMenuKey is the prefix sequence that shows the named
// configurations menu. Not a key anyone can type: the menu opens only when
// the list arrives (debugConfigsMsg), and resolveCommand builds it from
// Model.debugConfigMenu rather than from the static command tree.
const debugConfigsMenuKey = "<debug-configurations>"

// debugConfigsMsg carries the workspace's named configurations.
type debugConfigsMsg struct {
	bufID   uint32
	configs []DebugConfig
	err     error
}

// executeDebugConfigs fetches the named configurations; the menu opens when
// they arrive. Fetched each time rather than cached, so an edit to
// .indigo/debug.toml shows up on the next Space d l.
func executeDebugConfigs(m Model) (tea.Model, tea.Cmd) {
	if m.rpc == nil {
		return m, nil
	}
	rpc, bufID, file := m.rpc, m.bufID, m.filePath
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cfgs, err := rpc.DebugConfigs(ctx, file)
		return debugConfigsMsg{bufID: bufID, configs: cfgs, err: err}
	}
}

func (m Model) handleDebugConfigs(msg debugConfigsMsg) Model {
	if msg.bufID != m.bufID {
		return m
	}
	if msg.err != nil {
		m = m.pushStatus("E: debug configurations: " + msg.err.Error())
	}
	if len(msg.configs) == 0 {
		if msg.err == nil {
			m = m.pushStatus("No debug configurations: add [[debug]] entries to .indigo/debug.toml")
		}
		return m
	}
	m.debugConfigMenu = msg.configs
	// Only when nothing else took the keyboard while the list was on its way.
	if m.mode == ModeNormal && len(m.prefixSeq) == 0 {
		m.prefixSeq = []string{debugConfigsMenuKey}
	}
	return m
}

// debugConfigsCommand is the menu of named configurations, keyed 1-9 then a-z.
func debugConfigsCommand(cfgs []DebugConfig) command {
	const keys = "123456789abcdefghijklmnopqrstuvwxyz"
	node := command{key: debugConfigsMenuKey, label: "Debug configuration", menuTitle: "Debug configuration"}
	for i, cfg := range cfgs {
		if i >= len(keys) {
			break
		}
		label := cfg.Name
		if cfg.Mode == "test" {
			label += " (test)"
		}
		node.children = append(node.children, command{
			key:   string(keys[i]),
			label: label,
			execute: func(m Model) (tea.Model, tea.Cmd) {
				return m.startDebugging(cfg)
			},
		})
	}
	return node
}

func debugControl(action DebugAction, what string) func(Model) (tea.Model, tea.Cmd) {
	return func(m Model) (tea.Model, tea.Cmd) {
		if !m.debug.sessionActive() {
			return m.pushStatus("No debug session is running (Space d d starts one)"), nil
		}
		rpc := m.rpc
		return m, m.debugCmd(what, 15*time.Second, func(ctx context.Context) error {
			return rpc.DebugControl(ctx, action)
		})
	}
}

var (
	executeDebugContinue = debugControl(DebugContinue, "continue")
	executeDebugNext     = debugControl(DebugNext, "step over")
	executeDebugStepIn   = debugControl(DebugStepIn, "step in")
	executeDebugStepOut  = debugControl(DebugStepOut, "step out")
	executeDebugPause    = debugControl(DebugPause, "pause")
	executeDebugStop     = debugControl(DebugStop, "stop")
)

// executeDebugContinueOrStart is F5: continue a session, or start the last
// configuration again (this package's, the first time).
func executeDebugContinueOrStart(m Model) (tea.Model, tea.Cmd) {
	if m.debug.sessionActive() {
		return executeDebugContinue(m)
	}
	return executeDebugRestart(m)
}

// debugMenu is the Space-d submenu.
var debugMenu = command{
	key:       "d",
	label:     "Debug",
	menuTitle: "Debug",
	children: []command{
		{key: "b", name: "debug-toggle-breakpoint", label: "Toggle breakpoint", execute: executeToggleBreakpoint},
		{key: "B", name: "debug-breakpoint-condition", label: "Breakpoint condition…", execute: executeBreakpointCondition},
		{key: "L", name: "debug-logpoint", label: "Logpoint (log instead of stopping)…", execute: executeLogpoint},
		{key: "d", name: "debug-start", label: "Debug this package (or file)", execute: executeDebugStart},
		{key: "t", name: "debug-test", label: "Debug the test at the cursor", execute: executeDebugTest},
		{key: "l", name: "debug-configurations", label: "Debug a named configuration…", execute: executeDebugConfigs},
		{key: "r", name: "debug-restart", label: "Restart (the last configuration)", execute: executeDebugRestart},
		{key: "c", name: "debug-continue", label: "Continue", execute: executeDebugContinue},
		{key: "n", name: "debug-step-over", label: "Step over", execute: executeDebugNext},
		{key: "i", name: "debug-step-in", label: "Step in", execute: executeDebugStepIn},
		{key: "o", name: "debug-step-out", label: "Step out", execute: executeDebugStepOut},
		{key: "p", name: "debug-pause", label: "Pause", execute: executeDebugPause},
		{key: "x", name: "debug-stop", label: "Stop debugging", execute: executeDebugStop},
	},
}

// ---- hover evaluation ----

// expressionAtCursor is what hover evaluates while stopped: the selection if it
// is on one line, otherwise the identifier under the cursor together with any
// selector chain to its left — cfg.Name, not just Name, with the cursor on
// Name.
func (m Model) expressionAtCursor() string {
	if m.sel != nil {
		start, end := m.sel.ordered()
		if start.Line == end.Line {
			line := []rune(m.buf.Line(start.Line))
			if start.Col < len(line) {
				return strings.TrimSpace(string(line[start.Col:min(end.Col+1, len(line))]))
			}
		}
	}
	line := []rune(m.buf.Line(m.cursor.Line))
	col := m.cursor.Col
	if col >= len(line) || !isWordChar(line[col]) {
		return ""
	}
	end := col
	for end < len(line) && isWordChar(line[end]) {
		end++
	}
	start := col
	for start > 0 && (isWordChar(line[start-1]) || line[start-1] == '.') {
		start--
	}
	return strings.Trim(string(line[start:end]), ".")
}

// debugHoverCmd evaluates the expression at the cursor in the stopped frame,
// falling back to the language server's hover when it is not something the
// debugger can evaluate (a keyword, a type name, a function).
func (m Model) debugHoverCmd() tea.Cmd {
	expr := m.expressionAtCursor()
	lspHover := m.fetchHover()
	if expr == "" || m.rpc == nil {
		return lspHover
	}
	rpc, bufID, at := m.rpc, m.bufID, m.cursor
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		v, err := rpc.DebugEvaluate(ctx, expr, 0, "hover")
		if err != nil {
			if lspHover != nil {
				return lspHover()
			}
			return nil
		}
		text := expr + " = " + v.Value
		if v.Type != "" {
			text += "\n\n" + v.Type
		}
		return hoverMsg{result: ClientHoverResult{Found: true, Contents: text}, bufID: bufID, at: at}
	}
}
