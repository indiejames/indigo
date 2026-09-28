package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
)

// The process picker: which running program to attach the debugger to. Opened
// by Space d a (Go, with "connect to host:port" as well) and by a
// configuration that asks for a process when started (launch.json's
// ${command:pickProcess}). The list comes from the server — the machine the
// debugger runs on, a container's processes inside one.

type procPicker struct {
	cfg        client.DebugConfig // started with the chosen process id filled in
	allowTyped bool               // Space d a: a typed host:port or pid is a choice too
	filtered   bool               // show only programs the debugger can attach to (Tab toggles)

	procs   []client.DebugProcess
	loading bool
	err     string
	seq     int

	query  string
	cursor int
	width  int
	height int
}

// procRow is one choice: a listed process, or something typed.
type procRow struct {
	proc  *client.DebugProcess
	typed client.DebugConfig // set for a typed host:port or process id
	label string
}

type procListMsg struct {
	seq   int
	procs []client.DebugProcess
	err   error
}

// openProcPicker opens the picker for cfg and starts fetching the list.
func (a App) openProcPicker(cfg client.DebugConfig, allowTyped bool) (App, tea.Cmd) {
	seq := 1
	if a.procPicker != nil {
		seq = a.procPicker.seq + 1
	}
	a.procPicker = &procPicker{
		cfg: cfg, allowTyped: allowTyped,
		// Filtered to what the debugger can attach to — see debuggable — and
		// Tab shows everything.
		filtered: debuggableFilter(cfg, allowTyped) != nil,
		loading:  true, seq: seq, width: a.width, height: a.height,
	}
	rpc := a.rpc
	if rpc == nil {
		return a, nil
	}
	return a, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		procs, err := rpc.ListProcesses(ctx)
		return procListMsg{seq: seq, procs: procs, err: err}
	}
}

func (a App) handleProcList(msg procListMsg) App {
	p := a.procPicker
	if p == nil || msg.seq != p.seq {
		return a // closed, or superseded by a reopen
	}
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
	}
	p.procs = msg.procs
	p.clamp()
	return a
}

// rows is what is shown for the current query and filter.
func (p *procPicker) rows() []procRow {
	var rows []procRow
	q := strings.TrimSpace(p.query)
	if p.allowTyped && q != "" {
		if cfg, err := client.ParseAttachTarget(q); err == nil {
			switch {
			case cfg.Connect != "":
				rows = append(rows, procRow{typed: cfg, label: "Connect to the Delve at " + cfg.Connect})
			case !p.listed(cfg.ProcessID):
				rows = append(rows, procRow{typed: cfg, label: fmt.Sprintf("Attach to process %d", cfg.ProcessID)})
			}
		}
	}
	words := strings.Fields(strings.ToLower(q))
	for i := range p.procs {
		pr := &p.procs[i]
		if p.filtered && !p.debuggable(*pr) {
			continue
		}
		hay := strings.ToLower(fmt.Sprintf("%d %s %s %s", pr.PID, pr.Name, pr.GoModule, strings.Join(pr.Args, " ")))
		match := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				match = false
				break
			}
		}
		if match {
			rows = append(rows, procRow{proc: pr})
		}
	}
	return rows
}

func (p *procPicker) listed(pid int) bool {
	for _, pr := range p.procs {
		if pr.PID == pid {
			return true
		}
	}
	return false
}

func (p *procPicker) clamp() {
	n := len(p.rows())
	p.cursor = max(0, min(p.cursor, n-1))
}

// choice is the configuration for the selected row, and false when there is
// nothing to choose.
func (p *procPicker) choice() (client.DebugConfig, bool) {
	rows := p.rows()
	if p.cursor < 0 || p.cursor >= len(rows) {
		return client.DebugConfig{}, false
	}
	r := rows[p.cursor]
	if r.proc == nil {
		return r.typed, true
	}
	cfg := p.cfg
	if p.allowTyped && r.proc.IsNode() {
		// Space d a on a Node program: js-debug rather than Delve, with
		// nothing to configure — indigo switches its inspector on.
		cfg = client.DebugConfig{Adapter: "node"}
	}
	cfg.Request = "attach"
	cfg.ProcessID = r.proc.PID
	cfg.PickProcess = false
	return cfg, true
}

// handleProcPickerKey routes keys to the picker.
func (a App) handleProcPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.procPicker
	switch msg.String() {
	case "esc", "ctrl+c":
		a.procPicker = nil
	case "enter":
		cfg, ok := p.choice()
		if !ok {
			return a, nil
		}
		a.procPicker = nil
		return a, a.attachCmd(cfg)
	case "up", "ctrl+p":
		p.cursor = max(0, p.cursor-1)
	case "down", "ctrl+n":
		p.cursor++
		p.clamp()
	case "tab":
		p.filtered = !p.filtered
		p.cursor = 0
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if t := msg.Key().Text; t != "" {
			p.query += t
			p.cursor = 0
		}
	}
	return a, nil
}

// attachResultMsg reports an attach started from the picker.
type attachResultMsg struct {
	what    string
	warning string // attached, but something will not work (breakpoints under tsx)
	err     error
}

func (a App) attachCmd(cfg client.DebugConfig) tea.Cmd {
	rpc := a.rpc
	if rpc == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		warning, err := rpc.DebugStartWithWarning(ctx, cfg)
		return attachResultMsg{what: cfg.Describe(), warning: warning, err: err}
	}
}

const procPickerMaxVisible = 16

var (
	procPickerDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("#778899"))
	procPickerGo   = lipgloss.NewStyle().Foreground(lipgloss.Color("#66BBAA"))
	procPickerErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("#E05252"))
	procPickerHint = lipgloss.NewStyle().Background(bufPickerBg).Foreground(lipgloss.Color("#778899")).Padding(0, 1)
)

func (p *procPicker) render() string {
	innerW := max(min(p.width*3/4, 110), 50)
	title := "Attach to a process"
	if p.cfg.Name != "" {
		title = "Attach " + p.cfg.Name + " to a process"
	}
	kinds := p.kinds()
	filter := kinds + " · Tab: all processes"
	if !p.filtered {
		filter = "all processes"
		if kinds != "" {
			filter += " · Tab: " + kinds + " only"
		}
	}
	var rows []string
	rows = append(rows, bufPickerTitleStyle.Width(innerW).Render(title))
	prompt := "› " + p.query + "█"
	if p.query == "" {
		hint := "type to filter"
		if p.allowTyped {
			hint = "type to filter, or a pid or host:port"
		}
		prompt = "› " + procPickerDim.Render(hint)
	}
	rows = append(rows, bufPickerItemStyle.Width(innerW).Render(prompt))
	rows = append(rows, procPickerHint.Width(innerW).Render(filter))
	rows = append(rows, lipgloss.NewStyle().Background(bufPickerBg).Foreground(lipgloss.Color("#4488CC")).
		Render(strings.Repeat("─", innerW)))

	list := p.rows()
	switch {
	case p.loading:
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(procPickerDim.Render("reading processes…")))
	case p.err != "":
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(procPickerErr.Render(p.err)))
	case len(list) == 0 && p.filtered:
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(procPickerDim.Render("no "+p.kinds()+" match — Tab shows every process")))
	case len(list) == 0:
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(procPickerDim.Render("nothing matches")))
	}
	vis := visibleRows(len(list), procPickerMaxVisible, p.height, 6)
	start := max(0, min(p.cursor-vis/2, len(list)-vis))
	end := min(start+vis, len(list))
	if start > 0 || end < len(list) {
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(moreLabel(start > 0, "↑")))
	}
	for i := start; i < end; i++ {
		label := procLabel(list[i], innerW-2)
		if i == p.cursor {
			rows = append(rows, bufPickerSelStyle.Width(innerW).Render(ansi.Strip(label)))
		} else {
			rows = append(rows, bufPickerItemStyle.Width(innerW).Render(label))
		}
	}
	if start > 0 || end < len(list) {
		rows = append(rows, bufPickerItemStyle.Width(innerW).Render(moreLabel(end < len(list), "↓")))
	}
	return bufPickerBorderStyle.Render(strings.Join(rows, "\n"))
}

// procLabel is a row's text, cut to width: pid, name, the Go module and
// toolchain when there is one, then the arguments, which tell two copies of
// the same program apart.
func procLabel(r procRow, width int) string {
	if r.proc == nil {
		return truncateRunes(r.label, width)
	}
	pr := r.proc
	head := fmt.Sprintf("%7s  %s", strconv.Itoa(pr.PID), pr.Name)
	var goPart string
	if pr.IsGo() {
		goPart = "  " + pr.GoModule + " (" + pr.GoVersion + ")"
	}
	args := ""
	if len(pr.Args) > 0 {
		args = "  " + strings.Join(pr.Args, " ")
	}
	plain := truncateRunes(head+goPart+args, width)
	// Style only what survived the cut, by length.
	n := len([]rune(plain))
	h, g := len([]rune(head)), len([]rune(goPart))
	r0 := []rune(plain)
	out := string(r0[:min(n, h)])
	if n > h {
		out += procPickerGo.Render(string(r0[h:min(n, h+g)]))
	}
	if n > h+g {
		out += procPickerDim.Render(string(r0[h+g:]))
	}
	return out
}

func truncateRunes(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:max(width, 0)])
	}
	return string(r[:width-1]) + "…"
}

// debuggableFilter is the filter a picker applies before Tab: which processes the
// debugger it will start can attach to. nil means no filter (an adapter indigo
// cannot judge, so everything is listed).
func debuggableFilter(cfg client.DebugConfig, spaceDA bool) func(client.DebugProcess) bool {
	switch {
	case spaceDA:
		// Space d a attaches Go programs with Delve and Node programs with
		// js-debug, choosing by what was picked.
		return func(p client.DebugProcess) bool { return p.IsGo() || p.IsNode() }
	case cfg.Adapter == "" || cfg.Adapter == "go":
		return client.DebugProcess.IsGo
	case cfg.Adapter == "node":
		return client.DebugProcess.IsNode
	}
	return nil
}

func (p *procPicker) debuggable(pr client.DebugProcess) bool {
	f := debuggableFilter(p.cfg, p.allowTyped)
	return f == nil || f(pr)
}

// kinds names what the filter shows, for the picker's hint line.
func (p *procPicker) kinds() string {
	switch {
	case p.allowTyped:
		return "Go and Node programs"
	case p.cfg.Adapter == "" || p.cfg.Adapter == "go":
		return "Go programs"
	case p.cfg.Adapter == "node":
		return "Node programs"
	}
	return ""
}
