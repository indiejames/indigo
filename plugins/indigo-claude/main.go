package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/server"
)

// ─── conversation message types ───────────────────────────────────────────────

type MsgRole int

const (
	RoleStatus          MsgRole = iota // system/context updates, shown dimmed
	RoleUser                           // user input
	RoleAssistant                      // Claude response
	RoleTool                           // tool call notification
	RolePermission                     // pending file edit approval
	RoleShellPermission                // pending shell command approval
)

type ConvMsg struct {
	Role      MsgRole
	Content   string
	StartedAt time.Time // non-zero for in-progress tool entries
}

// ─── tea messages ────────────────────────────────────────────────────────────

type tickMsg struct{}
type activeCtxUpdated struct {
	ctx client.ActiveContext
	sel client.ActiveSelection
}

// ─── model ───────────────────────────────────────────────────────────────────

type Model struct {
	rpc     *client.RPC
	prog    *programLink
	apiKey  string // non-empty = API mode; empty = CLI mode
	workDir string

	width    int
	height   int
	conv     []ConvMsg
	input    []rune
	inputPos int

	// API mode state.
	history     []apiMessage
	pendingPerm *permissionRequestMsg

	// CLI mode hook state: queued shell permission requests shown one at a time.
	shellPermQueue []shellPermissionRequestMsg

	// CLI mode state.
	sessionID string

	// streaming state (both modes)
	agentRunning      bool
	agentRunningSince time.Time // when the current turn started; drives the rotating status word
	streamingConvIdx  int

	activeCtx client.ActiveContext
	activeSel client.ActiveSelection
	scroll    int
	ready     bool

	// Input history (most recent last); historyIdx -1 = not browsing.
	inputHistory []string
	historyIdx   int
	savedInput   string

	// Collapsed user messages: conv index → true means the bubble is collapsed.
	// Only messages with more than collapseThreshold rendered lines are collapsible.
	collapsedMsgs map[int]bool

	// Permission dialog: false = "approve" highlighted, true = "reject" highlighted.
	permChoice bool

	// Settings panel: nil = closed. Opened/closed with Tab/Esc, reachable
	// regardless of agentRunning (unlike the main text input) since its
	// rows — autoapprove and model — are read fresh by their consumers and
	// don't need the agent to be idle to take effect.
	settingsPanel *settingsPanelState

	// Token/cost tracking. In API mode ctxTokens is a live snapshot of the
	// most recent request's context size (meaningful as a % of the window);
	// in CLI mode it's cumulative tokens spent this session (NOT context
	// occupancy — see renderHeader/agentUsageMsg). sessionCost accumulates
	// across turns.
	ctxTokens   int
	sessionCost float64
	ctxWarned   bool // 80% context warning already shown (API mode only)

	// Subscription plan usage (CLI mode). warned* hold the highest warn level
	// already announced for the current window; they reset when the window does.
	plan          planUsage
	warnedSession float64
	warnedWeekly  float64

	// model is an alias ("opus", "sonnet", "haiku", "fable") or a full model
	// ID; "" means the mode's default (defaultModel in API mode, whatever the
	// claude CLI is configured for in CLI mode). Set via /model, persisted
	// across restarts.
	model string
}

// helpText lists every slash command indigo-claude recognizes itself, for
// /help. Kept in sync by hand with the command checks in handleKey — there
// are few enough of these that a generated table would be more machinery
// than the thing it replaces.
func helpText() string {
	return "**Commands**\n\n" +
		"- `/help`, `/?` — show this list\n" +
		"- `/clear` — clear the conversation and start fresh\n" +
		"- `/copy` — copy the last response to the clipboard\n" +
		"- `/model [" + strings.Join(modelAliasOrder, "|") + "|<full-id>|default]` — show or change the model\n" +
		"- `/autoapprove [edits|shell|all] [on|off]` — show or change auto-approve; bare form shows current state\n" +
		"- `/quit` - quit indigo-claude\n" +
		"- `Tab` — open the settings panel (auto-approve + model toggles); works even while Claude is running\n\n" +
		"Anything else is sent to Claude as a normal message." 
}

// onOff renders a bool as "on"/"off" for status messages.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// modelDisplay returns the current model for the header/status line, falling
// back to a mode-appropriate default label when unset.
func (m Model) modelDisplay() string {
	if m.model != "" {
		return m.model
	}
	if m.apiKey != "" {
		return defaultModel
	}
	return "default"
}

// contextWindowTokens is the assumed model context window for warnings.
const contextWindowTokens = 200_000

// ctxWarnPct is the context-fill percentage that triggers a warning.
const ctxWarnPct = 80

// fmtTokens renders a token count compactly: 512, 34.2k.
func fmtTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

func newModel(rpc *client.RPC, prog *programLink, apiKey, workDir string) Model {
	mode := "CLI mode (Claude Code)"
	if apiKey != "" {
		mode = "API mode (buffer-aware edits)"
	}
	return Model{
		rpc:              rpc,
		prog:             prog,
		apiKey:           apiKey,
		workDir:          workDir,
		streamingConvIdx: -1,
		conv:             []ConvMsg{{Role: RoleStatus, Content: "Connected · " + mode}},
		collapsedMsgs:    map[int]bool{},
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{scheduleTick(), m.cmdPollActiveCtx()}
	if m.apiKey == "" {
		// CLI mode runs on a subscription — check plan limits at startup.
		cmds = append(cmds, fetchPlanUsageCmd())
	}
	return tea.Batch(cmds...)
}

// ─── update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		return m, nil

	case tickMsg:
		for i := range m.conv {
			if m.conv[i].Role == RoleTool && !m.conv[i].StartedAt.IsZero() {
				name := strings.TrimPrefix(m.conv[i].Content, "  ⟳ ")
				if j := strings.LastIndex(name, " ("); j >= 0 {
					name = name[:j]
				}
				elapsed := time.Since(m.conv[i].StartedAt).Round(time.Second)
				m.conv[i].Content = fmt.Sprintf("  ⟳ %s (%s)", name, elapsed)
			}
		}
		return m, tea.Batch(scheduleTick(), m.cmdPollActiveCtx())

	case activeCtxUpdated:
		prev := m.activeCtx
		m.activeCtx = msg.ctx
		m.activeSel = msg.sel
		if msg.ctx.Found && msg.ctx.FilePath != prev.FilePath {
			m.conv = append(m.conv, ConvMsg{
				Role:    RoleStatus,
				Content: "Active file: " + m.displayPath(msg.ctx.FilePath),
			})
		}
		return m, nil

	case agentTextDeltaMsg:
		if m.streamingConvIdx < 0 {
			m.conv = append(m.conv, ConvMsg{Role: RoleAssistant, Content: msg.text})
			m.streamingConvIdx = len(m.conv) - 1
		} else {
			m.conv[m.streamingConvIdx].Content += msg.text
		}
		return m, nil

	case agentToolStartMsg:
		m.conv = append(m.conv, ConvMsg{Role: RoleTool, Content: "  ⟳ " + msg.name, StartedAt: time.Now()})
		return m, nil

	case agentToolDoneMsg:
		for i := len(m.conv) - 1; i >= 0; i-- {
			if m.conv[i].Role == RoleTool && !m.conv[i].StartedAt.IsZero() {
				elapsed := time.Since(m.conv[i].StartedAt).Round(time.Millisecond)
				m.conv[i] = ConvMsg{Role: RoleTool, Content: fmt.Sprintf("  ✓ %s (%s)",
					strings.TrimPrefix(m.conv[i].Content, "  ⟳ "), elapsed)}
				break
			}
		}
		m.streamingConvIdx = -1
		return m, nil

	case permissionRequestMsg:
		m.pendingPerm = &msg
		return m, nil

	case shellPermissionRequestMsg:
		m.shellPermQueue = append(m.shellPermQueue, msg)
		return m, nil

	case agentDoneMsg:
		m.agentRunning = false
		m.streamingConvIdx = -1
		if msg.sessionID != "" {
			m.sessionID = msg.sessionID
		}
		if msg.history != nil {
			m.history = msg.history
		}
		cmds := []tea.Cmd{m.saveStateCmd()}
		if m.apiKey == "" {
			// Refresh plan usage after each turn — that's when it moves.
			cmds = append(cmds, fetchPlanUsageCmd())
		}
		return m, tea.Batch(cmds...)

	case agentUnknownEventMsg:
		return m, nil

	case planUsageMsg:
		// New window → forget what we already warned about for it.
		if msg.usage.SevenDayReset != m.plan.SevenDayReset {
			m.warnedWeekly = 0
		}
		if msg.usage.FiveHourReset != m.plan.FiveHourReset {
			m.warnedSession = 0
		}
		m.plan = msg.usage
		if lvl := crossedLevel(m.plan.SevenDayPct, m.warnedWeekly); lvl > 0 {
			m.warnedWeekly = lvl
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
				"⚠ Weekly plan usage at %.0f%% — resets %s.",
				m.plan.SevenDayPct, fmtReset(m.plan.SevenDayReset))})
		}
		if lvl := crossedLevel(m.plan.FiveHourPct, m.warnedSession); lvl > 0 {
			m.warnedSession = lvl
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
				"⚠ Session (5h) plan usage at %.0f%% — resets %s.",
				m.plan.FiveHourPct, fmtReset(m.plan.FiveHourReset))})
		}
		return m, nil

	case agentUsageMsg:
		if msg.ctxTokens > 0 {
			m.ctxTokens = msg.ctxTokens
		}
		m.sessionCost += msg.costUSD
		// Only API mode's ctxTokens is a live context-window snapshot (see
		// renderHeader). CLI mode's is cumulative tokens spent this session —
		// comparing it to contextWindowTokens would fire this warning almost
		// immediately on any real session and wrongly tell the user to
		// /clear, discarding a conversation that was likely never actually
		// close to the real limit.
		if m.apiKey != "" && !m.ctxWarned && m.ctxTokens >= contextWindowTokens*ctxWarnPct/100 {
			m.ctxWarned = true
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
				"⚠ Context is %d%% full (%s of %s tokens). Consider /clear to start fresh.",
				m.ctxTokens*100/contextWindowTokens, fmtTokens(m.ctxTokens), fmtTokens(contextWindowTokens))})
		}
		return m, nil

	case agentErrorMsg:
		m.agentRunning = false
		m.streamingConvIdx = -1
		text := "Error: " + msg.err.Error()
		if msg.friendly != "" {
			text = "⚠ " + msg.friendly
		}
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: text})
		// Restore the failed prompt into the input box for an easy retry,
		// unless the user has already started typing something else.
		if msg.prompt != "" && len(m.input) == 0 {
			m.input = []rune(msg.prompt)
			m.inputPos = len(m.input)
		}
		return m, nil

	case tea.MouseMsg:
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.scroll += m.convHeight() / 4
		case tea.MouseButtonWheelDown:
			m.scroll -= m.convHeight() / 4
			if m.scroll < 0 {
				m.scroll = 0
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func validateSlashCommand(command string) bool {
	validCommands := []string{"help", "?", "clear", "copy", "model", "autoapprove", "quit"}
	trimmed := strings.TrimLeft(command, "/")
	trimmed = strings.Split(trimmed, " ")[0]
	return slices.Contains(validCommands, trimmed)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingPerm != nil {
		return m.handlePermissionKey(msg)
	}
	if len(m.shellPermQueue) > 0 {
		return m.handleShellPermissionKey(msg)
	}
	if m.settingsPanel != nil {
		return m.handleSettingsPanelKey(msg)
	}
	if msg.Type == tea.KeyTab {
		// Tab has no other use in the main input, so it's free to open the
		// settings panel — including mid-turn, unlike slash commands.
		m.settingsPanel = &settingsPanelState{}
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.agentRunning {
			m.prog.cancelAgent()
			m.agentRunning = false
			m.streamingConvIdx = -1
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Cancelled."})
		} else {
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Press ctrl+q to quit."})
		}
		return m, nil

	case tea.KeyCtrlQ:
		return m, tea.Quit

	case tea.KeyEnter:
		if msg.Alt {
			// Alt+Enter: insert newline in multi-line input.
			m.input = append(m.input[:m.inputPos], append([]rune{'\n'}, m.input[m.inputPos:]...)...)
			m.inputPos++
			m.historyIdx = -1
			return m, nil
		}

		text := strings.TrimSpace(string(m.input))
		if text == "" || m.agentRunning {
			return m, nil
		}

		if strings.HasPrefix(text, "/") {
			if !validateSlashCommand(text) {
				cmd := strings.Split(text, " ")[0]
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf("Unknown command %s", cmd)})
				return m, nil
			}
		}

		// Slash commands.
		if text == "/clear" {
			m.conv = []ConvMsg{{Role: RoleStatus, Content: "Conversation cleared."}}
			m.history = nil
			m.sessionID = ""
			m.input = nil
			m.inputPos = 0
			m.scroll = 0
			m.historyIdx = -1
			m.collapsedMsgs = map[int]bool{}
			m.ctxTokens = 0
			m.ctxWarned = false
			deleteState(m.workDir)
			return m, nil
		}
		if text == "/copy" {
			for i := len(m.conv) - 1; i >= 0; i-- {
				if m.conv[i].Role == RoleAssistant {
					copyToClipboard(m.conv[i].Content)
					m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Last response copied to clipboard."})
					break
				}
			}
			m.input = nil
			m.inputPos = 0
			m.historyIdx = -1
			return m, nil
		}
		if text == "/model" || strings.HasPrefix(text, "/model ") {
			arg := strings.TrimSpace(strings.TrimPrefix(text, "/model"))
			switch arg {
			case "":
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
					"Model: %s. Aliases: %s — or a full model ID. /model default to reset.",
					m.modelDisplay(), strings.Join(modelAliasOrder, ", "))})
			case "default", "reset":
				m.model = ""
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Model reset to default (" + m.modelDisplay() + ")."})
			default:
				m.model = arg
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Model set to " + arg + " — takes effect on your next message."})
			}
			m.input = nil
			m.inputPos = 0
			m.historyIdx = -1
			return m, m.saveStateCmd()
		}
		if text == "/autoapprove" || strings.HasPrefix(text, "/autoapprove ") {
			arg := strings.TrimSpace(strings.TrimPrefix(text, "/autoapprove"))
			edits, shell := m.prog.autoApprove()
			switch arg {
			case "":
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: fmt.Sprintf(
					"Auto-approve — edits: %s, shell: %s. Usage: /autoapprove [edits|shell|all] [on|off]. Resets to off on restart.",
					onOff(edits), onOff(shell))})
			case "on", "all on":
				m.prog.setAutoApproveEdits(true)
				m.prog.setAutoApproveShell(true)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve ON for edits and shell commands (this session only)."})
			case "off", "all off":
				m.prog.setAutoApproveEdits(false)
				m.prog.setAutoApproveShell(false)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve OFF — every edit and shell command will prompt again."})
			case "edits on":
				m.prog.setAutoApproveEdits(true)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve ON for edits (this session only). Shell commands still prompt."})
			case "edits off":
				m.prog.setAutoApproveEdits(false)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve OFF for edits."})
			case "shell on":
				m.prog.setAutoApproveShell(true)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve ON for shell commands (this session only). Edits still prompt."})
			case "shell off":
				m.prog.setAutoApproveShell(false)
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve OFF for shell commands."})
			default:
				m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Usage: /autoapprove [edits|shell|all] [on|off]"})
			}
			m.input = nil
			m.inputPos = 0
			m.historyIdx = -1
			return m, nil
		}
		if text == "/help" || text == "/?" {
			m.conv = append(m.conv, ConvMsg{Role: RoleAssistant, Content: helpText()})
			m.input = nil
			m.inputPos = 0
			m.historyIdx = -1
			return m, nil
		}

		if text == "/quit" {
			 return m, tea.Quit
		}

		// Save to history (skip consecutive duplicates).
		if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
			m.inputHistory = append(m.inputHistory, text)
		}
		m.historyIdx = -1
		m.savedInput = ""

		m.conv = append(m.conv, ConvMsg{Role: RoleUser, Content: text})
		m.collapsedMsgs[len(m.conv)-1] = true // collapsed by default once sent
		m.input = nil
		m.inputPos = 0
		m.scroll = 0
		m.streamingConvIdx = -1
		m.agentRunning = true
		m.agentRunningSince = time.Now()

		prog := m.prog
		ac := m.activeCtx
		sel := m.activeSel
		model := m.model
		if m.apiKey != "" {
			// Copy history so the goroutine's append can't share a backing
			// array with m.history.
			history := make([]apiMessage, len(m.history))
			copy(history, m.history)
			go runAgent(prog, m.rpc, m.apiKey, model, m.workDir, history, text, ac, sel)
		} else {
			go runClaudeSubprocess(prog, m.rpc, m.workDir, text, m.sessionID, model, ac, sel)
		}
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		if m.inputPos > 0 {
			m.input = append(m.input[:m.inputPos-1], m.input[m.inputPos:]...)
			m.inputPos--
			m.historyIdx = -1
		}
		return m, nil

	case tea.KeyLeft:
		if m.inputPos > 0 {
			m.inputPos--
		}
		return m, nil

	case tea.KeyRight:
		if m.inputPos < len(m.input) {
			m.inputPos++
		}
		return m, nil

	case tea.KeyHome, tea.KeyCtrlA:
		m.inputPos = inputLineStart(m.input, m.inputPos)
		return m, nil

	case tea.KeyEnd, tea.KeyCtrlE:
		m.inputPos = inputLineEnd(m.input, m.inputPos)
		return m, nil

	case tea.KeyUp:
		if msg.Alt {
			// Alt+Up: step back through input history.
			if len(m.inputHistory) == 0 {
				return m, nil
			}
			if m.historyIdx == -1 {
				m.savedInput = string(m.input)
				m.historyIdx = len(m.inputHistory) - 1
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input = []rune(m.inputHistory[m.historyIdx])
			m.inputPos = len(m.input)
			return m, nil
		}
		m.inputPos = inputCursorUp(m.input, m.inputPos)
		return m, nil

	case tea.KeyDown:
		if msg.Alt {
			// Alt+Down: step forward through input history.
			if m.historyIdx == -1 {
				return m, nil
			}
			if m.historyIdx >= len(m.inputHistory)-1 {
				m.input = []rune(m.savedInput)
				m.inputPos = len(m.input)
				m.historyIdx = -1
				return m, nil
			}
			m.historyIdx++
			m.input = []rune(m.inputHistory[m.historyIdx])
			m.inputPos = len(m.input)
			return m, nil
		}
		m.inputPos = inputCursorDown(m.input, m.inputPos)
		return m, nil

	case tea.KeyPgUp:
		m.scroll += m.convHeight() / 2
		return m, nil

	case tea.KeyPgDown:
		m.scroll -= m.convHeight() / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil

	case tea.KeySpace:
		m.input = append(m.input[:m.inputPos], append([]rune{' '}, m.input[m.inputPos:]...)...)
		m.inputPos++
		m.historyIdx = -1
		return m, nil

	case tea.KeyRunes:
		if msg.Alt {
			// Alt+e: toggle expand/collapse of the topmost visible user message.
			if len(msg.Runes) == 1 && msg.Runes[0] == 'e' {
				m.toggleTopUserMsg()
			}
			return m, nil
		}
		s := msg.String()
		// Fast wheel scrolling can overwhelm the terminal parser and leak SGR
		// mouse sequences ("<65;70;21M") into the rune stream. Strip them from
		// typed input; pasted text is left untouched.
		if !msg.Paste && len(msg.Runes) > 1 {
			s = stripMouseArtifacts(s)
			if s == "" {
				return m, nil
			}
		}
		r := []rune(s)
		m.input = append(m.input[:m.inputPos], append(r, m.input[m.inputPos:]...)...)
		m.inputPos += len(r)
		m.historyIdx = -1
		return m, nil
	}
	return m, nil
}

// mouseSeqRe matches fragments of SGR mouse escape sequences that survive when
// the ESC[ prefix was consumed elsewhere: "<65;70;21M", "[<64;10;5M", etc.
var mouseSeqRe = regexp.MustCompile(`\[?<?\d{1,3};\d{1,3};\d{1,3}[Mm]`)

// stripMouseArtifacts removes leaked SGR mouse-event fragments from s.
func stripMouseArtifacts(s string) string {
	return mouseSeqRe.ReplaceAllString(s, "")
}

func (m Model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyRight:
		m.permChoice = !m.permChoice
		return m, nil
	case tea.KeyEnter:
		// fall through to confirm
	default:
		// y/n shortcuts still work.
		switch msg.String() {
		case "y", "Y":
			m.permChoice = false
		case "n", "N":
			m.permChoice = true
		default:
			return m, nil
		}
	}
	perm := m.pendingPerm
	m.pendingPerm = nil
	approved := !m.permChoice
	m.permChoice = false
	label := "Rejected: " + perm.file
	if approved {
		label = "Approved: " + perm.file
	}
	m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: label})
	perm.replyCh <- approved
	return m, nil
}

func (m Model) handleShellPermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyLeft, tea.KeyRight:
		m.permChoice = !m.permChoice
		return m, nil
	case tea.KeyEnter:
		// fall through to confirm
	default:
		switch msg.String() {
		case "y", "Y":
			m.permChoice = false
		case "n", "N":
			m.permChoice = true
		default:
			return m, nil
		}
	}
	perm := m.shellPermQueue[0]
	m.shellPermQueue = m.shellPermQueue[1:]
	approved := !m.permChoice
	m.permChoice = false
	label := "Command rejected."
	if approved {
		label = "Command approved."
	}
	m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: label})
	perm.replyCh <- approved
	return m, nil
}

// ─── settings panel ─────────────────────────────────────────────────────────

// settingsPanelRowCount is the number of rows in the settings panel: auto-
// approve edits, auto-approve shell, model.
const settingsPanelRowCount = 3

// settingsPanelState tracks which row of the settings panel has focus.
type settingsPanelState struct {
	focus int
}

// settingsPanelModelChoices is the model cycle order for the settings panel's
// model row: "" (mode default) followed by each alias in modelAliasOrder.
var settingsPanelModelChoices = append([]string{""}, modelAliasOrder...)

// nextModelChoice steps current by dir (+1/-1) through settingsPanelModelChoices,
// wrapping at either end. A value not in the list (e.g. a full model ID set
// via /model) is treated as if it were "default" for the purposes of cycling.
func nextModelChoice(current string, dir int) string {
	idx := slices.Index(settingsPanelModelChoices, current)
	if idx == -1 {
		idx = 0
	}
	n := len(settingsPanelModelChoices)
	idx = (idx + dir + n) % n
	return settingsPanelModelChoices[idx]
}

// adjustSettingsRow applies dir (+1/-1) to the currently focused settings
// panel row: toggles the two auto-approve rows regardless of dir's sign
// (there are only two states), and steps the model row through
// settingsPanelModelChoices.
func (m Model) adjustSettingsRow(dir int) (tea.Model, tea.Cmd) {
	switch m.settingsPanel.focus {
	case 0:
		edits, _ := m.prog.autoApprove()
		m.prog.setAutoApproveEdits(!edits)
	case 1:
		_, shell := m.prog.autoApprove()
		m.prog.setAutoApproveShell(!shell)
	case 2:
		m.model = nextModelChoice(m.model, dir)
		return m, m.saveStateCmd()
	}
	return m, nil
}

func (m Model) handleSettingsPanelKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.settingsPanel = nil
		return m, nil
	case tea.KeyTab:
		m.settingsPanel.focus = (m.settingsPanel.focus + 1) % settingsPanelRowCount
		return m, nil
	case tea.KeyShiftTab:
		m.settingsPanel.focus = (m.settingsPanel.focus - 1 + settingsPanelRowCount) % settingsPanelRowCount
		return m, nil
	case tea.KeyLeft:
		return m.adjustSettingsRow(-1)
	case tea.KeyRight, tea.KeyEnter, tea.KeySpace:
		return m.adjustSettingsRow(1)
	}
	return m, nil
}

// ─── view ────────────────────────────────────────────────────────────────────

var (
	headerStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#087AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#667788"))
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#AABBCC"))
	inputBorderSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#335577"))
	inputPromptSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#087AC8")).Bold(true)
	cursorStyle    = lipgloss.NewStyle().Reverse(true)

	// User message bubble — slightly elevated surface colour.
	bubbleBg = lipgloss.AdaptiveColor{Dark: "#1B2939", Light: "#DDE8F4"}
	// Progressive foreground dimming for collapsed bubbles (bright → dim).
	bubbleFg  = lipgloss.AdaptiveColor{Dark: "#C8D8E8", Light: "#2A3A4A"}
	bubbleDim = []lipgloss.TerminalColor{
		lipgloss.AdaptiveColor{Dark: "#C8D8E8", Light: "#2A3A4A"}, // line 1: full
		lipgloss.AdaptiveColor{Dark: "#8898A8", Light: "#627282"}, // line 2: medium
		lipgloss.AdaptiveColor{Dark: "#4A5A6A", Light: "#96A6B6"}, // line 3: dim
	}
	bubbleBorderColor = lipgloss.AdaptiveColor{Dark: "#3D5472", Light: "#7A9DC0"}
	bubbleHintColor   = lipgloss.AdaptiveColor{Dark: "#445566", Light: "#8899AA"}

	// Permission popup — matches the indigo editor popup palette.
	ppBg     = lipgloss.Color("#1E2A38")
	ppBorder = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#4488CC"))
	ppKey    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	ppLabel  = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#CCDDEC")).Bold(true)
	ppText   = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#CCDDEE"))
	ppOld    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FF5555"))
	ppNew    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#55FF55"))
)

func (m Model) View() string {
	if !m.ready {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.renderConversation())
	sb.WriteByte('\n')
	sb.WriteString(m.renderInput()) // bordered box; always exactly inputHeight() rows
	sb.WriteByte('\n')
	sb.WriteString(m.renderHeader()) // status bar at the bottom, like indigo
	base := sb.String()
	if m.pendingPerm != nil || len(m.shellPermQueue) > 0 {
		base = m.overlayPermPopup(base)
	} else if m.settingsPanel != nil {
		base = m.overlaySettingsPanel(base)
	}
	return base
}

// expandTabs replaces tab characters with spaces out to the next 4-column
// tab stop. lipgloss.Width counts a tab as a single narrow rune, but a
// terminal renders it at a tab stop — so a diff line containing a literal
// tab (near-guaranteed in tab-indented source) throws off the popup's
// fixed-width padding and leaves its right border ragged. Expanding tabs to
// spaces before measuring/rendering keeps the counted and rendered widths in
// sync.
func expandTabs(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	const tabWidth = 4
	var sb strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - (col % tabWidth)
			sb.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		sb.WriteRune(r)
		col++
	}
	return sb.String()
}

// buildPermPopup returns the styled lines of the permission popup box.
func (m Model) buildPermPopup() []string {
	const maxInnerW = 64
	const maxBodyRows = 20 // absolute cap; a permission prompt should stay skimmable

	var title string
	var bodyLines []string // plain text, styled during render

	switch {
	case m.pendingPerm != nil:
		title = "⚠ Edit request"
		if len(m.pendingPerm.edits) == 0 {
			title = "⚠ Save request"
		}
		bodyLines = append(bodyLines, "  File: "+m.pendingPerm.file)
		if m.pendingPerm.reason != "" {
			bodyLines = append(bodyLines, "  Reason: "+m.pendingPerm.reason)
		}
		for _, e := range m.pendingPerm.edits {
			// Pure insertions (insert_at_line) have no old text to show.
			if e.oldText != "" {
				bodyLines = append(bodyLines, "  ── remove ──")
				for _, l := range strings.Split(e.oldText, "\n") {
					bodyLines = append(bodyLines, "  - "+l)
				}
			}
			bodyLines = append(bodyLines, "  ── add ──")
			for _, l := range strings.Split(e.newText, "\n") {
				bodyLines = append(bodyLines, "  + "+l)
			}
		}
	case len(m.shellPermQueue) > 0:
		title = "⚠ Shell command"
		bodyLines = append(bodyLines, "  $ "+m.shellPermQueue[0].command)
	default:
		return nil
	}

	for i, l := range bodyLines {
		bodyLines[i] = expandTabs(l)
	}

	// Cap the body height so the popup (footer included) always fits above
	// the input box, however large the diff is. availRows leaves room for
	// the title, the separator above the footer, the footer itself, and the
	// bottom border.
	availRows := m.convHeight() - 4
	if availRows < 3 {
		availRows = 3
	}
	capRows := min(maxBodyRows, availRows)
	if len(bodyLines) > capRows {
		shown := max(capRows-1, 1) // reserve one row for the "more" indicator
		hidden := len(bodyLines) - shown
		more := fmt.Sprintf("  … %d more line", hidden)
		if hidden != 1 {
			more += "s"
		}
		more += " (approve to apply the full change)"
		bodyLines = append(bodyLines[:shown], more)
	}

	footerStr := "  ◀ approve ▶   ◀ reject ▶"

	// innerW = visible chars between the │ borders
	innerW := lipgloss.Width(title) + 3 // "─ " + title + " " at minimum
	for _, l := range bodyLines {
		if w := lipgloss.Width(l); w > innerW {
			innerW = w
		}
	}
	if w := lipgloss.Width(footerStr); w > innerW {
		innerW = w
	}
	innerW = min(innerW, min(maxInnerW, m.width-8))
	innerW = max(innerW, 32)

	// Top border: ╭─ ⚠(yellow) rest-of-title(label) ────╮
	// title is "⚠ Edit request" or "⚠ Shell command"
	titleStyled := ppKey.Render("⚠") + ppLabel.Render(strings.TrimPrefix(title, "⚠"))
	remainDashes := max(0, innerW-lipgloss.Width(title)-3)
	topLine := ppBorder.Render("╭─ ") + titleStyled + ppBorder.Render(" "+strings.Repeat("─", remainDashes)+"╮")

	var out []string
	out = append(out, topLine)

	for _, bl := range bodyLines {
		// Truncate overlong lines to fit innerW
		if lipgloss.Width(bl) > innerW {
			bl = ansi.Truncate(bl, innerW-1, "…")
		}
		pad := max(0, innerW-lipgloss.Width(bl))
		// Include padding inside the styled span so the popup background covers it.
		var mid string
		switch {
		case strings.HasPrefix(bl, "  - "):
			mid = ppOld.Render(bl + strings.Repeat(" ", pad))
		case strings.HasPrefix(bl, "  + "):
			mid = ppNew.Render(bl + strings.Repeat(" ", pad))
		default:
			mid = ppText.Render(bl + strings.Repeat(" ", pad))
		}
		out = append(out, ppBorder.Render("│")+mid+ppBorder.Render("│"))
	}

	// Separator + footer with highlighted selection (◀ active choice ▶).
	selStyle := lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	dimStyle := ppText
	var approveStyled, rejectStyled string
	if !m.permChoice { // approve highlighted
		approveStyled = selStyle.Render("◀ approve ▶")
		rejectStyled = dimStyle.Render("  reject  ")
	} else { // reject highlighted
		approveStyled = dimStyle.Render("  approve  ")
		rejectStyled = selStyle.Render("◀ reject ▶")
	}
	footerRendered := ppText.Render("  ") + approveStyled + ppText.Render("   ") + rejectStyled
	footerPad := max(0, innerW-lipgloss.Width(footerStr))
	out = append(out, ppBorder.Render("├"+strings.Repeat("─", innerW)+"┤"))
	out = append(out, ppBorder.Render("│")+footerRendered+ppText.Render(strings.Repeat(" ", footerPad))+ppBorder.Render("│"))
	out = append(out, ppBorder.Render("╰"+strings.Repeat("─", innerW)+"╯"))

	return out
}

// overlayPopupAbove places popup's lines just above the input box, horizontally
// centered. Shared by the permission popup and the settings panel.
func (m Model) overlayPopupAbove(base string, popup []string) string {
	if len(popup) == 0 {
		return base
	}

	popW := 0
	for _, l := range popup {
		if w := lipgloss.Width(l); w > popW {
			popW = w
		}
	}

	lines := strings.Split(base, "\n")
	totalH := len(lines)
	startCol := max(0, (m.width-popW)/2)
	// Position popup so its bottom edge sits just above the input box top border.
	// The input box occupies rows [convHeight .. convHeight+inputHeight-1] (0-indexed).
	// We want the popup to end at row convHeight-1.
	inputBoxTopRow := m.convHeight()
	startRow := max(1, inputBoxTopRow-len(popup))

	for i, popLine := range popup {
		row := startRow + i
		if row >= totalH {
			break
		}
		bg := lines[row]
		// ansi.Truncate gives us the first startCol visual columns; pad if shorter.
		left := ansi.Truncate(bg, startCol, "")
		leftW := lipgloss.Width(left)
		if leftW < startCol {
			left += strings.Repeat(" ", startCol-leftW)
		}
		lines[row] = left + popLine
	}
	return strings.Join(lines, "\n")
}

// overlayPermPopup places the permission popup just above the input box.
func (m Model) overlayPermPopup(base string) string {
	return m.overlayPopupAbove(base, m.buildPermPopup())
}

// overlaySettingsPanel places the settings panel just above the input box.
func (m Model) overlaySettingsPanel(base string) string {
	return m.overlayPopupAbove(base, m.buildSettingsPopup())
}

// buildSettingsPopup returns the styled lines of the settings panel box:
// auto-approve edits/shell toggles and the model selector, all editable
// without needing the agent to be idle.
func (m Model) buildSettingsPopup() []string {
	if m.settingsPanel == nil {
		return nil
	}
	edits, shell := m.prog.autoApprove()
	modelLabel := m.model
	if modelLabel == "" {
		modelLabel = "default"
	}
	rows := []struct{ label, value string }{
		{"Auto-approve edits", onOff(edits)},
		{"Auto-approve shell", onOff(shell)},
		{"Model", modelLabel},
	}

	labelW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r.label); w > labelW {
			labelW = w
		}
	}
	lineFor := func(r struct{ label, value string }) string {
		label := r.label + strings.Repeat(" ", labelW-lipgloss.Width(r.label))
		return "  " + label + "  " + r.value
	}

	const titleText = "⚙ Settings"
	const footerText = "  Tab next   ←/→ change   Esc close"
	innerW := lipgloss.Width(titleText) + 3
	for _, r := range rows {
		if w := lipgloss.Width(lineFor(r)); w > innerW {
			innerW = w
		}
	}
	if w := lipgloss.Width(footerText); w > innerW {
		innerW = w
	}
	innerW = min(innerW, m.width-8)
	innerW = max(innerW, 32)

	titleStyled := ppKey.Render("⚙") + ppLabel.Render(strings.TrimPrefix(titleText, "⚙"))
	remainDashes := max(0, innerW-lipgloss.Width(titleText)-3)
	topLine := ppBorder.Render("╭─ ") + titleStyled + ppBorder.Render(" "+strings.Repeat("─", remainDashes)+"╮")

	selStyle := lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)

	var out []string
	out = append(out, topLine)
	for i, r := range rows {
		line := lineFor(r)
		if i == m.settingsPanel.focus {
			line = "▸ " + line[len("  "):]
		}
		pad := max(0, innerW-lipgloss.Width(line))
		padded := line + strings.Repeat(" ", pad)
		style := ppText
		if i == m.settingsPanel.focus {
			style = selStyle
		}
		out = append(out, ppBorder.Render("│")+style.Render(padded)+ppBorder.Render("│"))
	}
	footerPad := max(0, innerW-lipgloss.Width(footerText))
	out = append(out, ppBorder.Render("├"+strings.Repeat("─", innerW)+"┤"))
	out = append(out, ppBorder.Render("│")+ppText.Render(footerText+strings.Repeat(" ", footerPad))+ppBorder.Render("│"))
	out = append(out, ppBorder.Render("╰"+strings.Repeat("─", innerW)+"╯"))
	return out
}

// thinkingWords rotates through while the agent is working, in the spirit of
// Claude.ai / the VS Code extension's status line: a mix of plausible verbs
// and a few goofy ones, so a long-running turn doesn't just sit on
// "Thinking…" the whole time.
var thinkingWords = []string{
	"Thinking",
	"Pondering",
	"Percolating",
	"Noodling",
	"Ruminating",
	"Cogitating",
	"Marinating",
	"Contemplating",
	"Puzzling",
	"Mulling",
	"Synthesizing",
	"Deliberating",
	"Channeling",
	"Conjuring",
	"Divining",
	"Wrangling",
	"Untangling",
	"Brewing",
	"Excogitating",
	"Vibing",
	"Ruminating further",
	"Herding electrons",
	"Consulting the rubber duck",
	"Summoning tokens",
}

// thinkingWord picks a status word based on how long the current turn has
// been running, rotating to the next word every few seconds. A zero since
// (no turn in progress yet) always returns the first word.
func thinkingWord(since time.Time) string {
	if since.IsZero() {
		return thinkingWords[0]
	}
	const rotateEvery = 2500 * time.Millisecond
	idx := int(time.Since(since)/rotateEvery) % len(thinkingWords)
	return thinkingWords[idx]
}

func (m Model) renderHeader() string {
	label := " indigo-claude"
	if m.activeCtx.Found {
		label = fmt.Sprintf(" indigo-claude   %s %d:%d",
			m.displayPath(m.activeCtx.FilePath), m.activeCtx.Line+1, m.activeCtx.Col+1)
		if m.activeSel.Found && m.activeSel.BufID == m.activeCtx.BufID {
			label += fmt.Sprintf("  ·  sel %d–%d", m.activeSel.StartLine+1, m.activeSel.EndLine+1)
		}
	}
	if m.pendingPerm != nil || len(m.shellPermQueue) > 0 {
		label += "  [approve? y/n]"
	} else if m.agentRunning {
		label += "  [" + thinkingWord(m.agentRunningSince) + "…]"
	}

	// Right side: model (only shown once overridden via /model, to avoid
	// clutter for the common case) plus context stats and the plan-limit
	// warning. Cost appears only in API mode where tokens are billed
	// directly. The plan warning prefers its full text with reset times; on
	// narrow terminals it degrades to compact chips, then drops entirely
	// rather than truncating the label.
	var ctxSeg string
	if m.model != "" {
		ctxSeg = m.model
	}
	if autoEdits, autoShell := m.prog.autoApprove(); autoEdits || autoShell {
		if ctxSeg != "" {
			ctxSeg += " · "
		}
		switch {
		case autoEdits && autoShell:
			ctxSeg += "⚠ auto-approve: all"
		case autoEdits:
			ctxSeg += "⚠ auto-approve: edits"
		default:
			ctxSeg += "⚠ auto-approve: shell"
		}
	}
	if m.ctxTokens > 0 {
		if ctxSeg != "" {
			ctxSeg += " · "
		}
		if m.apiKey != "" {
			// API mode: ctxTokens is a snapshot of the most recent request's
			// actual context size (see streamUsageEvent in api.go), so a
			// percentage of the window is meaningful.
			pct := m.ctxTokens * 100 / contextWindowTokens
			if pct >= ctxWarnPct {
				ctxSeg += "⚠ "
			}
			ctxSeg += fmt.Sprintf("%s ctx (%d%%)", fmtTokens(m.ctxTokens), pct)
			if m.sessionCost > 0 {
				ctxSeg += fmt.Sprintf(" · $%.2f", m.sessionCost)
			}
		} else {
			// CLI mode: the stream-json "result" event's usage sums every
			// internal tool-call round-trip within a turn, so ctxTokens is
			// cumulative tokens spent this session, not live context
			// occupancy — showing it as "% of contextWindowTokens" compares
			// unrelated quantities and reliably overshoots 100%. Claude
			// Code's own CLI process handles real context/compaction
			// internally; this is just an FYI spend counter.
			ctxSeg += fmt.Sprintf("%s tokens this session", fmtTokens(m.ctxTokens))
		}
	}

	labelW := lipgloss.Width(label)
	for _, plan := range []string{m.planWarnHint(), m.planWarnChips(), ""} {
		right := ctxSeg
		if plan != "" {
			if right != "" {
				right += " · "
			}
			right += plan
		}
		if right != "" {
			right += " "
		}
		if pad := m.width - labelW - lipgloss.Width(right); pad >= 1 {
			return headerStyle.Render(label + strings.Repeat(" ", pad) + right)
		}
	}
	return headerStyle.Width(m.width).Render(label)
}

func (m Model) renderConversation() string {
	h := m.convHeight()
	if h <= 0 {
		return ""
	}

	allLines, userStarts := m.renderAllLinesIndexed()
	total := len(allLines)
	maxScroll := max(0, total-h)
	scroll := min(m.scroll, maxScroll)
	visibleStart := max(0, total-h-scroll)

	// Find the user message to pin: the most-recent one whose natural start
	// line lies strictly above the visible area's top edge.
	pinnedConvIdx := -1
	for _, s := range userStarts {
		if s.startLine < visibleStart {
			pinnedConvIdx = s.convIdx
		}
	}

	// writeLines writes exactly count rows to sb (padding blank rows at the top
	// when src has fewer than count lines). Each row is separated by '\n' with no
	// trailing '\n' after the last row, so the caller can safely append '\n'+more.
	writeLines := func(sb *strings.Builder, src []string, count int) {
		lineIdx := 0
		for i := 0; i < count-len(src); i++ {
			if lineIdx > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(strings.Repeat(" ", m.width))
			lineIdx++
		}
		for _, l := range src {
			if lineIdx > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(padRight(strings.TrimRight(l, "\r"), m.width))
			lineIdx++
		}
	}

	var rows strings.Builder

	if pinnedConvIdx < 0 {
		// No pinning — normal scroll view.
		end := min(visibleStart+h, total)
		visible := allLines[visibleStart:end]
		writeLines(&rows, visible, h)
		return rows.String()
	}

	// Render the pinned bubble (always in its current collapsed/expanded state).
	pinLines := m.renderUserMsg(pinnedConvIdx, m.conv[pinnedConvIdx], m.width)
	pinH := len(pinLines)
	scrollH := h - pinH
	if scrollH < 0 {
		scrollH = 0
	}

	// Write pinned lines with '\n' between them (no trailing '\n').
	for i, l := range pinLines {
		if i > 0 {
			rows.WriteByte('\n')
		}
		rows.WriteString(padRight(strings.TrimRight(l, "\r"), m.width))
	}

	if scrollH > 0 {
		rows.WriteByte('\n') // separator between pinned and scrolled content
		// Recompute the window for the reduced height scrollH — reusing the
		// outer visibleStart (sized for the full height h) would leave the
		// window pinH lines short of the true bottom, clipping the tail of
		// the most recent content.
		scrollStart := max(0, total-scrollH-scroll)
		end := min(scrollStart+scrollH, total)
		visible := allLines[scrollStart:end]
		writeLines(&rows, visible, scrollH)
	}

	return rows.String()
}

// userMsgStart records where a user message begins in the flat line array.
type userMsgStart struct {
	convIdx   int
	startLine int
}

// renderAllLinesIndexed builds all conversation lines and returns where each
// user message starts, so renderConversation can pin the right one.
func (m Model) renderAllLinesIndexed() (lines []string, starts []userMsgStart) {
	w := m.width
	if w < 8 {
		w = 8
	}
	for i, msg := range m.conv {
		if i > 0 {
			lines = append(lines, "")
		}
		if msg.Role == RoleUser {
			starts = append(starts, userMsgStart{convIdx: i, startLine: len(lines)})
			lines = append(lines, m.renderUserMsg(i, msg, w)...)
		} else {
			lines = append(lines, m.renderMsg(msg, w)...)
		}
	}
	return
}

// renderUserMsg renders a user message as a full-width rounded bubble with
// progressive dimming when collapsed. collapseThreshold is the minimum number
// of content lines before collapsing takes effect.
const collapseThreshold = 3

func (m Model) renderUserMsg(convIdx int, msg ConvMsg, w int) []string {
	const margin = 2
	boxW := w - margin*2 // total box width including both border chars
	if boxW < 8 {
		boxW = 8
	}
	// inner content width: boxW minus left-border(1)+left-pad(1)+right-pad(1)+right-border(1)
	contentW := boxW - 4
	if contentW < 1 {
		contentW = 1
	}

	// Word-wrap raw content into content-width lines.
	var contentLines []string
	for _, para := range strings.Split(msg.Content, "\n") {
		if para == "" {
			contentLines = append(contentLines, "")
			continue
		}
		contentLines = append(contentLines, wordWrap(para, contentW)...)
	}
	if len(contentLines) == 0 {
		contentLines = []string{""}
	}

	left := strings.Repeat(" ", margin)
	bdrSty := lipgloss.NewStyle().Background(bubbleBg).Foreground(bubbleBorderColor)
	hintSty := lipgloss.NewStyle().Background(bubbleBg).Foreground(bubbleHintColor).Italic(true)

	// Render one content line with the given foreground colour and bubble background.
	renderLine := func(text string, fg lipgloss.TerminalColor) string {
		sty := lipgloss.NewStyle().Background(bubbleBg).Foreground(fg)
		pad := strings.Repeat(" ", max(0, contentW-lipgloss.Width(text)))
		// " " + text + padding + " " fills the full inner area including padding cols.
		return left + bdrSty.Render("│") + sty.Render(" "+text+pad+" ") + bdrSty.Render("│")
	}

	dashes := strings.Repeat("─", boxW-2)
	topLine := left + bdrSty.Render("╭"+dashes+"╮")
	botLine := left + bdrSty.Render("╰"+dashes+"╯")

	collapsed := m.collapsedMsgs[convIdx] && len(contentLines) > collapseThreshold

	var out []string
	out = append(out, topLine)

	if collapsed {
		for i := 0; i < collapseThreshold; i++ {
			line := contentLines[i]
			if lipgloss.Width(line) > contentW {
				line = ansi.Truncate(line, contentW-1, "…")
			}
			fg := bubbleDim[min(i, len(bubbleDim)-1)]
			out = append(out, renderLine(line, fg))
		}
		remaining := len(contentLines) - collapseThreshold
		hint := fmt.Sprintf("… %d more lines  [alt+e] expand", remaining)
		hintPad := strings.Repeat(" ", max(0, contentW-lipgloss.Width(hint)))
		out = append(out, left+bdrSty.Render("│")+hintSty.Render(" "+hint+hintPad+" ")+bdrSty.Render("│"))
	} else {
		for _, line := range contentLines {
			if lipgloss.Width(line) > contentW {
				line = ansi.Truncate(line, contentW-1, "…")
			}
			out = append(out, renderLine(line, bubbleFg))
		}
		// Show collapse hint only when the message is long enough to be collapsible.
		if len(contentLines) > collapseThreshold {
			hint := "[alt+e] collapse"
			hintPad := strings.Repeat(" ", max(0, contentW-lipgloss.Width(hint)))
			out = append(out, left+bdrSty.Render("│")+hintSty.Render(" "+hint+hintPad+" ")+bdrSty.Render("│"))
		}
	}

	out = append(out, botLine)
	return out
}

// toggleTopUserMsg flips the collapsed state of whichever user message is
// currently pinned at the top of the view, or the topmost visible one if none.
func (m Model) toggleTopUserMsg() {
	allLines, starts := m.renderAllLinesIndexed()
	if len(starts) == 0 {
		return
	}
	h := m.convHeight()
	total := len(allLines)
	maxScroll := max(0, total-h)
	scroll := min(m.scroll, maxScroll)
	visibleStart := max(0, total-h-scroll)

	target := -1
	for _, s := range starts {
		if s.startLine < visibleStart {
			target = s.convIdx // keep updating → last user msg above viewport
		}
	}
	if target == -1 {
		// Nothing pinned; pick the topmost user message that is visible.
		for _, s := range starts {
			if s.startLine >= visibleStart {
				target = s.convIdx
				break
			}
		}
	}
	if target >= 0 {
		m.collapsedMsgs[target] = !m.collapsedMsgs[target]
	}
}

func (m Model) renderMsg(msg ConvMsg, w int) []string {
	switch msg.Role {
	case RoleStatus:
		return []string{padRight(statusStyle.Render("  · "+msg.Content), w)}

	case RoleAssistant:
		// No label — just the markdown content, like a chat response.
		return renderMarkdown(msg.Content, w)

	case RoleTool:
		return []string{padRight(toolStyle.Render(msg.Content), w)}

	}
	return nil
}

func (m Model) renderInput() string {
	h := m.inputHeight()  // total rows including top and bottom border
	contentH := h - 2     // inner content rows
	innerW := m.width - 2 // inner width (m.width minus two '│' border chars)
	if innerW < 1 {
		innerW = 1
	}

	// Embed scroll/thinking hint in the top border. Plan-limit warnings live
	// in the status bar at the bottom.
	var hintStr string
	switch {
	case m.agentRunning:
		hintStr = " thinking… "
	case m.scroll > 0:
		hintStr = " PgUp/PgDn to scroll "
	}
	hintW := lipgloss.Width(hintStr)
	topDashes := max(0, innerW-1-hintW)
	topBorder := inputBorderSty.Render("╭─" + hintStr + strings.Repeat("─", topDashes) + "╮")
	botBorder := inputBorderSty.Render("╰" + strings.Repeat("─", innerW) + "╯")

	prompt := inputPromptSty.Render("▶ ")
	pw := lipgloss.Width(prompt)
	cont := strings.Repeat(" ", pw)

	dispLines, cursorRow, cursorCol := wrapInputDisplay(m.input, m.inputPos, m.inputAvailWidth())

	totalLines := len(dispLines)
	winStart := 0
	if totalLines > contentH {
		winStart = cursorRow - contentH + 1
		if winStart < 0 {
			winStart = 0
		}
		if winStart+contentH > totalLines {
			winStart = totalLines - contentH
		}
	}
	winEnd := min(totalLines, winStart+contentH)

	var sb strings.Builder
	sb.WriteString(topBorder)

	for i := winStart; i < winEnd; i++ {
		sb.WriteByte('\n')
		var prefix string
		if i == 0 {
			prefix = prompt
		} else {
			prefix = cont
		}

		lineRunes := dispLines[i]
		var lineContent string
		if i == cursorRow {
			col := min(cursorCol, len(lineRunes))
			before := string(lineRunes[:col])
			var curChar, after string
			if col < len(lineRunes) {
				curChar = cursorStyle.Render(string(lineRunes[col]))
				after = string(lineRunes[col+1:])
			} else {
				curChar = cursorStyle.Render(" ")
			}
			lineContent = before + curChar + after
		} else {
			lineContent = string(lineRunes)
		}

		lineW := lipgloss.Width(prefix + lineContent)
		pad := strings.Repeat(" ", max(0, innerW-lineW))
		sb.WriteString(inputBorderSty.Render("│"))
		sb.WriteString(prefix + lineContent + pad)
		sb.WriteString(inputBorderSty.Render("│"))
	}

	// Pad remaining content rows with empty bordered lines.
	for i := winEnd - winStart; i < contentH; i++ {
		sb.WriteByte('\n')
		sb.WriteString(inputBorderSty.Render("│"))
		sb.WriteString(strings.Repeat(" ", innerW))
		sb.WriteString(inputBorderSty.Render("│"))
	}

	sb.WriteByte('\n')
	sb.WriteString(botBorder)
	return sb.String()
}

// ─── layout helpers ──────────────────────────────────────────────────────────

// inputHeight returns the number of terminal rows the input box occupies,
// including the top and bottom border rows. Minimum is 3 (border + 1 line + border).
// Grows with content up to a third of the screen height.
func (m Model) inputHeight() int {
	maxContentH := max(1, m.height/3-2) // max inner content rows
	dispLines, _, _ := wrapInputDisplay(m.input, m.inputPos, m.inputAvailWidth())
	return min(len(dispLines), maxContentH) + 2 // +2 for top and bottom border
}

// convHeight returns the number of rows available for conversation display.
// Layout: conv(convHeight) + '\n' + input(inputHeight) + '\n' + header(1)
// Total rows = convHeight + inputHeight + 1 (header only; no separate divider row).
func (m Model) convHeight() int {
	return max(1, m.height-m.inputHeight()-1)
}

// ─── input cursor movement helpers ───────────────────────────────────────────

// inputCursorUp moves pos to the same column on the previous line, or returns
// pos unchanged if already on the first line.
func inputCursorUp(input []rune, pos int) int {
	before := string(input[:pos])
	beforeLines := strings.Split(before, "\n")
	curLine := len(beforeLines) - 1
	if curLine == 0 {
		return pos
	}
	curCol := len([]rune(beforeLines[curLine]))
	allLines := strings.Split(string(input), "\n")
	prevLen := len([]rune(allLines[curLine-1]))
	targetCol := min(curCol, prevLen)
	newPos := 0
	for i := 0; i < curLine-1; i++ {
		newPos += len([]rune(allLines[i])) + 1 // +1 for '\n'
	}
	return newPos + targetCol
}

// inputCursorDown moves pos to the same column on the next line, or returns
// pos unchanged if already on the last line.
func inputCursorDown(input []rune, pos int) int {
	before := string(input[:pos])
	beforeLines := strings.Split(before, "\n")
	curLine := len(beforeLines) - 1
	curCol := len([]rune(beforeLines[curLine]))
	allLines := strings.Split(string(input), "\n")
	if curLine >= len(allLines)-1 {
		return pos
	}
	nextLen := len([]rune(allLines[curLine+1]))
	targetCol := min(curCol, nextLen)
	newPos := 0
	for i := 0; i <= curLine; i++ {
		newPos += len([]rune(allLines[i])) + 1
	}
	return newPos + targetCol
}

// inputLineStart returns the index of the first rune on the current line.
func inputLineStart(input []rune, pos int) int {
	for pos > 0 && input[pos-1] != '\n' {
		pos--
	}
	return pos
}

// inputLineEnd returns the index just past the last rune on the current line
// (i.e. at the '\n' or at len(input)).
func inputLineEnd(input []rune, pos int) int {
	for pos < len(input) && input[pos] != '\n' {
		pos++
	}
	return pos
}

// inputAvailWidth returns the number of columns available for input text
// content inside the input box, after the border and prompt/continuation gutter.
func (m Model) inputAvailWidth() int {
	innerW := m.width - 2
	if innerW < 1 {
		innerW = 1
	}
	pw := lipgloss.Width(inputPromptSty.Render("▶ "))
	avail := innerW - pw
	if avail < 1 {
		avail = 1
	}
	return avail
}

// wrapLineOffsets returns the rune offsets, within a single logical (no '\n')
// line, at which word-wrapped display rows begin. Every rune of line belongs
// to exactly one resulting row, so cursor positions map back exactly.
func wrapLineOffsets(line []rune, width int) []int {
	if width < 1 {
		width = 1
	}
	n := len(line)
	if n == 0 {
		return []int{0}
	}
	offsets := []int{0}
	start := 0
	for n-start > width {
		end := start + width - 1 // last index that keeps the row within width
		breakAt := -1
		for k := end; k > start; k-- {
			if line[k] == ' ' {
				breakAt = k
				break
			}
		}
		var next int
		if breakAt == -1 {
			next = start + width
		} else {
			next = breakAt + 1
		}
		// Trailing spaces at a wrap point are invisible either way, so fold
		// them into the row being closed rather than starting a blank row.
		for next < n && line[next] == ' ' {
			next++
		}
		offsets = append(offsets, next)
		start = next
	}
	return offsets
}

// wrapInputDisplay word-wraps input (which may contain '\n') into display
// rows no wider than width runes, and reports the display row/col of pos.
func wrapInputDisplay(input []rune, pos int, width int) (lines [][]rune, cursorRow, cursorCol int) {
	n := len(input)
	i := 0
	for {
		j := i
		for j < n && input[j] != '\n' {
			j++
		}
		line := input[i:j]
		offsets := wrapLineOffsets(line, width)
		for k, off := range offsets {
			segEnd := len(line)
			if k+1 < len(offsets) {
				segEnd = offsets[k+1]
			}
			lines = append(lines, line[off:segEnd])

			absStart, absEnd := i+off, i+segEnd
			isLastSeg := k == len(offsets)-1
			if (pos >= absStart && pos < absEnd) || (isLastSeg && pos == absEnd) {
				cursorRow, cursorCol = len(lines)-1, pos-absStart
			}
		}
		if j >= n {
			break
		}
		i = j + 1
	}
	return lines, cursorRow, cursorCol
}

// ─── commands ────────────────────────────────────────────────────────────────

func scheduleTick() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(250 * time.Millisecond)
		return tickMsg{}
	}
}

func (m Model) cmdPollActiveCtx() tea.Cmd {
	rpc := m.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		ac, err := rpc.GetActiveContext(ctx)
		if err != nil {
			return nil
		}
		sel, _ := rpc.GetActiveSelection(ctx) // zero value on error = no selection
		return activeCtxUpdated{ctx: ac, sel: sel}
	}
}

// ─── utilities ───────────────────────────────────────────────────────────────

// copyToClipboard writes text to the system clipboard, trying pbcopy (macOS),
// xclip, and xsel in order.
func copyToClipboard(text string) {
	for _, args := range [][]string{
		{"pbcopy"},
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return
		}
	}
}

func (m Model) displayPath(filePath string) string {
	if rel, err := filepath.Rel(m.workDir, filePath); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return filepath.Base(filePath)
}

func wordWrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := ""
		for _, w := range words {
			ww := len([]rune(w))
			if cur == "" {
				cur = w
			} else if len([]rune(cur))+1+ww <= width {
				cur += " " + w
			} else {
				lines = append(lines, cur)
				cur = w
			}
		}
		if cur != "" {
			lines = append(lines, cur)
		}
	}
	return lines
}

// ─── markdown rendering ──────────────────────────────────────────────────────

// mdCache holds a glamour renderer for the last-seen width. View() is
// single-threaded (Bubble Tea main loop), so no lock is needed.
var mdCache struct {
	width int
	r     *glamour.TermRenderer
}

func getMarkdownRenderer(width int) *glamour.TermRenderer {
	if mdCache.r == nil || mdCache.width != width {
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return nil
		}
		mdCache.r = r
		mdCache.width = width
	}
	return mdCache.r
}

// renderMarkdown runs content through glamour and returns terminal lines.
// Falls back to plain word-wrap if glamour fails.
func renderMarkdown(content string, width int) []string {
	r := getMarkdownRenderer(width)
	if r == nil {
		return wordWrap(content, width)
	}
	rendered, err := r.Render(content)
	if err != nil {
		return wordWrap(content, width)
	}
	// Glamour wraps output in blank lines; trim them.
	rendered = strings.TrimRight(rendered, "\n")
	lines := strings.Split(rendered, "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

func padRight(s string, width int) string {
	vis := lipgloss.Width(s)
	if vis >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vis)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ─── lock file ───────────────────────────────────────────────────────────────

func acquireWorkspaceLock(sockPath string) (*os.File, error) {
	lockPath := filepath.Join(filepath.Dir(sockPath), "claude.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close() //nolint:errcheck
		return nil, fmt.Errorf("another indigo-claude is already running for this workspace")
	}
	return f, nil
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	// Hook-client mode: invoked by the PreToolUse hook script.
	if len(os.Args) == 3 && os.Args[1] == "--hook" {
		runHookClient(os.Args[2])
		return
	}

	// MCP server mode: spawned by the claude CLI; speaks MCP over stdio and
	// forwards tool calls to the running TUI via the Unix socket.
	if len(os.Args) == 3 && os.Args[1] == "--mcp" {
		runMCPServer(os.Args[2])
		return
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: cannot determine working directory: %v\n", err)
		os.Exit(1)
	}

	sockPath := server.SocketPath(workDir)
	if !server.IsRunning(sockPath) {
		fmt.Fprintf(os.Stderr, "indigo-claude: no indigo server found in %s\n", workDir)
		fmt.Fprintf(os.Stderr, "Start indigo first, then run indigo-claude in the same directory.\n")
		os.Exit(1)
	}

	lockFile, err := acquireWorkspaceLock(sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: %v\n", err)
		os.Exit(1)
	}
	defer lockFile.Close() //nolint:errcheck

	rpc, err := client.Dial(sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: cannot connect: %v\n", err)
		os.Exit(1)
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	prog := &programLink{}

	// Set up PreToolUse permission hook and the MCP tool bridge (best-effort;
	// CLI mode only). Both share the same Unix socket served by this process.
	// Everything lives in a private 0700 directory with an unpredictable name
	// so other local users can neither connect to the socket nor squat its
	// path (same defense as indigo's own server socket dir).
	if runtimeDir, err := os.MkdirTemp("", "indigo-claude-"); err == nil {
		defer os.RemoveAll(runtimeDir) //nolint:errcheck
		permSockPath := filepath.Join(runtimeDir, "perm.sock")
		hookScriptPath := filepath.Join(runtimeDir, "indigo-claude-hook.sh")
		mcpConfigPath := filepath.Join(runtimeDir, "mcp.json")
		if binaryPath, err := os.Executable(); err == nil {
			// Fail closed: the socket must be listening before the hook is
			// installed, so hook decisions can never come from a socket we
			// don't own.
			if ln, err := startPermissionServer(permSockPath, prog, rpc, workDir); err == nil {
				defer ln.Close() //nolint:errcheck
				if err := writeHookScript(hookScriptPath, binaryPath, permSockPath); err == nil {
					installHook(workDir, hookScriptPath) //nolint:errcheck
					defer removeHook(workDir)
				}
				// Buffer-aware file tools for the claude subprocess: claude
				// spawns `indigo-claude --mcp <sock>`, which forwards tool
				// calls back to this process over the same socket.
				if err := writeMCPConfig(mcpConfigPath, binaryPath, permSockPath); err == nil {
					prog.mcpConfig = mcpConfigPath
				}
			}
		}
	}

	// Tell the indigo server we're connected so it can show an indicator in the
	// status bar. The key is arbitrary; the editor renders any non-empty text
	// set via SetStatusBarText as a status bar decoration.
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 3*time.Second)
	rpc.SetStatusBarText(connectCtx, "indigo-claude", " claude ◆ ") //nolint:errcheck
	connectCancel()

	model := newModel(rpc, prog, apiKey, workDir)
	model = model.restoreState(loadState(workDir, apiKey))
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithoutSignalHandler())
	rpc.SetPushSender(p.Send)

	prog.mu.Lock()
	prog.send = p.Send
	prog.mu.Unlock()

	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: %v\n", err)
		os.Exit(1)
	}
	// Persist the conversation on clean exit so a restart can resume it.
	if fm, ok := finalModel.(Model); ok && len(fm.conv) > 1 {
		writeState(snapshotState(fm)) //nolint:errcheck
	}

	// Clear the status bar entry before disconnecting; the server-side
	// clearForConn also handles crash recovery automatically.
	clearCtx, clearCancel := context.WithTimeout(context.Background(), 2*time.Second)
	rpc.SetStatusBarText(clearCtx, "indigo-claude", "") //nolint:errcheck
	clearCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rpc.Disconnect(ctx) //nolint:errcheck
}
