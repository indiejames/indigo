package main

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
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

	// mdRenderCache memoizes renderMarkdown by conv index, so View() — called
	// on every keystroke and status tick — doesn't re-run glamour (markdown
	// parse + syntax highlighting) over the entire conversation history each
	// time. Entries are invalidated automatically when content or width
	// changes (e.g. the in-progress streaming message, or a terminal resize).
	mdRenderCache map[int]mdCacheEntry

	// Permission dialog: false = "approve" highlighted, true = "reject" highlighted.
	permChoice bool

	// pendingAutoApproveRestore is non-nil when a restored workspace session
	// had auto-approve on, pending explicit confirmation before it's applied
	// (see restoreState/handleAutoApproveRestoreKey). autoApproveRestoreChoice:
	// false = "No" highlighted (the default), true = "Yes" highlighted.
	pendingAutoApproveRestore *autoApproveRestoreState
	autoApproveRestoreChoice  bool

	// focus selects which control receives non-global key input: the main
	// text box, or one of the always-visible top control-bar fields.
	// Tab/Shift+Tab cycle through all of them (see handleKey/handleBarFocusKey),
	// reachable regardless of agentRunning (unlike text submission) since the
	// bar's fields — autoapprove and model — are read fresh by their
	// consumers and don't need the agent to be idle to take effect.
	focus inputFocus

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
		"- `Tab` — cycle focus between the top control bar (auto-approve, model) and the message input; " +
		"←/→ change the focused field, Space/Enter also work; Esc returns focus to the input. Works even while Claude is running.\n\n" +
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
		mdRenderCache:    map[int]mdCacheEntry{},
	}
}

func (m Model) Init() tea.Cmd {
	// RequestBackgroundColor so light terminals get the light palette: v2
	// dropped lipgloss's AdaptiveColor, which used to make this query itself.
	cmds := []tea.Cmd{scheduleTick(), m.cmdPollActiveCtx(), tea.RequestBackgroundColor}
	if m.apiKey == "" {
		// CLI mode runs on a subscription — check plan limits at startup.
		cmds = append(cmds, fetchPlanUsageCmd())
	}
	return tea.Batch(cmds...)
}

// ─── update ──────────────────────────────────────────────────────────────────

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		if dark := msg.IsDark(); dark != adaptiveDark {
			adaptiveDark = dark
			refreshAdaptiveColors()
		}
		return m, nil

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

	case tea.MouseWheelMsg:
		switch msg.Mouse().Button {
		case tea.MouseWheelUp:
			m.scroll += m.convHeight() / 4
		case tea.MouseWheelDown:
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
	if m.pendingAutoApproveRestore != nil {
		return m.handleAutoApproveRestoreKey(msg)
	}
	if m.pendingPerm != nil {
		return m.handlePermissionKey(msg)
	}
	if len(m.shellPermQueue) > 0 {
		return m.handleShellPermissionKey(msg)
	}

	// These work regardless of which control has focus.
	switch msg.String() {
	case "ctrl+c":
		if m.agentRunning {
			m.prog.cancelAgent()
			m.agentRunning = false
			m.streamingConvIdx = -1
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Cancelled."})
		} else {
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Press ctrl+q to quit."})
		}
		return m, nil

	case "ctrl+q":
		return m, tea.Quit

	case "tab":
		// Tab has no other use in the main input, so it's free to cycle
		// focus into the control bar — including mid-turn, unlike slash
		// commands, since autoapprove/model are read fresh by consumers.
		m.focus = (m.focus + 1) % numFocusTargets
		return m, nil

	case "shift+tab":
		m.focus = (m.focus - 1 + numFocusTargets) % numFocusTargets
		return m, nil
	}

	if m.focus != focusTextInput {
		return m.handleBarFocusKey(msg)
	}

	switch msg.String() {
	case "alt+enter":
		// Alt+Enter: insert newline in multi-line input. v2 folds the alt
		// modifier into the key string, so this is its own case rather than
		// a msg.Alt branch inside "enter".
		m.input = append(m.input[:m.inputPos], append([]rune{'\n'}, m.input[m.inputPos:]...)...)
		m.inputPos++
		m.historyIdx = -1
		return m, nil

	case "enter":
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
			m.mdRenderCache = map[int]mdCacheEntry{}
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

	case "backspace", "delete":
		if m.inputPos > 0 {
			m.input = append(m.input[:m.inputPos-1], m.input[m.inputPos:]...)
			m.inputPos--
			m.historyIdx = -1
		}
		return m, nil

	case "left":
		if m.inputPos > 0 {
			m.inputPos--
		}
		return m, nil

	case "right":
		if m.inputPos < len(m.input) {
			m.inputPos++
		}
		return m, nil

	case "home", "ctrl+a":
		m.inputPos = inputLineStart(m.input, m.inputPos)
		return m, nil

	case "end", "ctrl+e":
		m.inputPos = inputLineEnd(m.input, m.inputPos)
		return m, nil

	case "alt+up":
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

	case "up":
		m.inputPos = inputCursorUp(m.input, m.inputPos)
		return m, nil

	case "alt+down":
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

	case "down":
		m.inputPos = inputCursorDown(m.input, m.inputPos)
		return m, nil

	case "pgup":
		m.scroll += m.convHeight() / 2
		return m, nil

	case "pgdown":
		m.scroll -= m.convHeight() / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil

	case "space":
		m.input = append(m.input[:m.inputPos], append([]rune{' '}, m.input[m.inputPos:]...)...)
		m.inputPos++
		m.historyIdx = -1
		return m, nil

	case "alt+e":
		// Alt+e: toggle expand/collapse of the topmost visible user message.
		m.toggleTopUserMsg()
		return m, nil

	default:
		// Any remaining key that produces text is literal input. v1 matched
		// tea.KeyRunes here; v2 has no such catch-all key type, so this is
		// the default branch gated on the key actually carrying text.
		s := msg.Key().Text
		if s == "" {
			return m, nil
		}
		// Fast wheel scrolling can overwhelm the terminal parser and leak SGR
		// mouse sequences ("<65;70;21M") into the typed-input stream. Strip
		// them. v1 also had to exclude pastes here; v2 delivers those as a
		// separate PasteMsg (handled above), so anything reaching this branch
		// is genuinely typed.
		if utf8.RuneCountInString(s) > 1 {
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
}

// mouseSeqRe matches fragments of SGR mouse escape sequences that survive when
// the ESC[ prefix was consumed elsewhere: "<65;70;21M", "[<64;10;5M", etc.
var mouseSeqRe = regexp.MustCompile(`\[?<?\d{1,3};\d{1,3};\d{1,3}[Mm]`)

// stripMouseArtifacts removes leaked SGR mouse-event fragments from s.
func stripMouseArtifacts(s string) string {
	return mouseSeqRe.ReplaceAllString(s, "")
}

func (m Model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right":
		m.permChoice = !m.permChoice
		return m, nil
	case "enter":
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
	switch msg.String() {
	case "left", "right":
		m.permChoice = !m.permChoice
		return m, nil
	case "enter":
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

// ─── auto-approve restore confirmation ──────────────────────────────────────

// autoApproveRestoreState holds the auto-approve values found in a restored
// workspace session, pending confirmation before they're applied — unlike
// the model field, silently reapplying this could skip approval for file
// edits or shell commands without the user noticing.
type autoApproveRestoreState struct {
	edits, shell bool
}

func (m Model) handleAutoApproveRestoreKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right":
		m.autoApproveRestoreChoice = !m.autoApproveRestoreChoice
		return m, nil
	case "enter":
		// fall through to confirm
	default:
		switch msg.String() {
		case "y", "Y":
			m.autoApproveRestoreChoice = true
		case "n", "N":
			m.autoApproveRestoreChoice = false
		default:
			return m, nil
		}
	}
	pending := m.pendingAutoApproveRestore
	restore := m.autoApproveRestoreChoice
	m.pendingAutoApproveRestore = nil
	m.autoApproveRestoreChoice = false
	if restore {
		m.prog.setAutoApproveEdits(pending.edits)
		m.prog.setAutoApproveShell(pending.shell)
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve restored from last session."})
	} else {
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Auto-approve left off."})
	}
	// Persist the decision either way, so declining doesn't leave a stale
	// "on" in the state file that would just ask again next time.
	return m, m.saveStateCmd()
}

// ─── control bar focus ──────────────────────────────────────────────────────

// inputFocus selects which control receives non-global key input.
type inputFocus int

const (
	focusTextInput inputFocus = iota
	focusAutoApproveEdits
	focusAutoApproveShell
	focusModel
	numFocusTargets // sentinel; keep last
)

// modelCycleChoices is the model cycle order for the control bar's model
// field: "" (mode default) followed by each alias in modelAliasOrder.
var modelCycleChoices = append([]string{""}, modelAliasOrder...)

// nextModelChoice steps current by dir (+1/-1) through modelCycleChoices,
// wrapping at either end. A value not in the list (e.g. a full model ID set
// via /model) is treated as if it were "default" for the purposes of cycling.
func nextModelChoice(current string, dir int) string {
	idx := slices.Index(modelCycleChoices, current)
	if idx == -1 {
		idx = 0
	}
	n := len(modelCycleChoices)
	idx = (idx + dir + n) % n
	return modelCycleChoices[idx]
}

// adjustFocusedControl applies dir (+1/-1) to the currently focused control-bar
// field: toggles the two auto-approve fields regardless of dir's sign (there
// are only two states), and steps the model field through modelCycleChoices.
func (m Model) adjustFocusedControl(dir int) (tea.Model, tea.Cmd) {
	switch m.focus {
	case focusAutoApproveEdits:
		edits, _ := m.prog.autoApprove()
		m.prog.setAutoApproveEdits(!edits)
		return m, m.saveStateCmd()
	case focusAutoApproveShell:
		_, shell := m.prog.autoApprove()
		m.prog.setAutoApproveShell(!shell)
		return m, m.saveStateCmd()
	case focusModel:
		m.model = nextModelChoice(m.model, dir)
		return m, m.saveStateCmd()
	}
	return m, nil
}

// handleBarFocusKey handles key input while a control-bar field (rather than
// the text input) has focus. Tab/Shift+Tab/Ctrl+C/Ctrl+Q are handled by the
// caller before this is reached, so they still work from here too.
func (m Model) handleBarFocusKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.focus = focusTextInput
		return m, nil
	case "left":
		return m.adjustFocusedControl(-1)
	case "right", "enter", "space":
		return m.adjustFocusedControl(1)
	}
	return m, nil
}

// ─── view ────────────────────────────────────────────────────────────────────

var (
	headerStyle = lipgloss.NewStyle().Background(lipgloss.Color("#087AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#667788"))
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#AABBCC"))
	// Input box: brighter border/prompt when it has focus (focusTextInput),
	// dimmer border/prompt/text when focus has moved to the control bar.
	inputBorderSty      = lipgloss.NewStyle().Foreground(lipgloss.Color("#335577"))            // unfocused
	inputBorderFocusSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#4488CC"))            // focused
	inputPromptSty      = lipgloss.NewStyle().Foreground(lipgloss.Color("#087AC8")).Bold(true) // focused
	inputPromptDimSty   = lipgloss.NewStyle().Foreground(lipgloss.Color("#335577")).Bold(true) // unfocused
	inputTextDimSty     = lipgloss.NewStyle().Foreground(lipgloss.Color("#667788"))            // unfocused
	cursorStyle         = lipgloss.NewStyle().Reverse(true)

	// User message bubble — slightly elevated surface colour. These are
	// rebuilt by refreshAdaptiveColors once the terminal reports its real
	// background, since adaptive() bakes in adaptiveDark at the moment it
	// is called.
	bubbleBg = adaptive("#1B2939", "#DDE8F4")
	// Progressive foreground dimming for collapsed bubbles (bright → dim).
	bubbleFg  = adaptive("#C8D8E8", "#2A3A4A")
	bubbleDim = []color.Color{
		adaptive("#C8D8E8", "#2A3A4A"), // line 1: full
		adaptive("#8898A8", "#627282"), // line 2: medium
		adaptive("#4A5A6A", "#96A6B6"), // line 3: dim
	}
	bubbleBorderColor = adaptive("#3D5472", "#7A9DC0")
	bubbleHintColor   = adaptive("#445566", "#8899AA")

	// Permission popup — matches the indigo editor popup palette.
	ppBg     = lipgloss.Color("#1E2A38")
	ppBorder = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#4488CC"))
	ppKey    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	ppLabel  = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#CCDDEC")).Bold(true)
	ppText   = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#CCDDEE"))
	ppOld    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FF5555"))
	ppNew    = lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#55FF55"))
)

// View returns the frame plus the terminal modes this plugin needs. Bubble
// Tea v2 moved these off tea.NewProgram's options and onto the View, so they
// are declared per-frame here — the direct equivalents of the
// WithAltScreen()/WithMouseCellMotion() this program passed under v1.
// Without MouseMode, mouse-wheel scrolling of the conversation stops working.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) render() string {
	if !m.ready {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.renderControlBar()) // k9s-style: always visible, top of screen
	sb.WriteByte('\n')
	sb.WriteString(m.renderConversation())
	sb.WriteByte('\n')
	sb.WriteString(m.renderInput()) // bordered box; always exactly inputHeight() rows
	sb.WriteByte('\n')
	sb.WriteString(m.renderHeader()) // status bar at the bottom, like indigo
	base := sb.String()
	if m.pendingPerm != nil || len(m.shellPermQueue) > 0 {
		base = m.overlayPermPopup(base)
	} else if m.pendingAutoApproveRestore != nil {
		base = m.overlayPopupAbove(base, m.buildAutoApproveRestorePopup())
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

// buildAutoApproveRestorePopup returns the styled lines of the restore
// confirmation dialog, shown once at startup when a restored workspace
// session had auto-approve on. "No" is the default-highlighted choice.
func (m Model) buildAutoApproveRestorePopup() []string {
	p := m.pendingAutoApproveRestore
	if p == nil {
		return nil
	}
	var which []string
	if p.edits {
		which = append(which, "edits")
	}
	if p.shell {
		which = append(which, "shell")
	}

	const title = "⚠ Restore auto-approve?"
	bodyLines := []string{
		"  Auto-approve (" + strings.Join(which, ", ") + ") was on last time",
		"  you used this workspace. Restore it?",
	}
	const footerStr = "  ◀ No ▶   ◀ Yes ▶"

	innerW := lipgloss.Width(title) + 3
	for _, l := range bodyLines {
		if w := lipgloss.Width(l); w > innerW {
			innerW = w
		}
	}
	if w := lipgloss.Width(footerStr); w > innerW {
		innerW = w
	}
	innerW = min(innerW, min(64, m.width-8))
	innerW = max(innerW, 32)

	titleStyled := ppKey.Render("⚠") + ppLabel.Render(strings.TrimPrefix(title, "⚠"))
	remainDashes := max(0, innerW-lipgloss.Width(title)-3)
	topLine := ppBorder.Render("╭─ ") + titleStyled + ppBorder.Render(" "+strings.Repeat("─", remainDashes)+"╮")

	var out []string
	out = append(out, topLine)
	for _, bl := range bodyLines {
		pad := max(0, innerW-lipgloss.Width(bl))
		out = append(out, ppBorder.Render("│")+ppText.Render(bl+strings.Repeat(" ", pad))+ppBorder.Render("│"))
	}

	selStyle := lipgloss.NewStyle().Background(ppBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	dimStyle := ppText
	var noStyled, yesStyled string
	if !m.autoApproveRestoreChoice { // "No" highlighted — the safe default
		noStyled = selStyle.Render("◀ No ▶")
		yesStyled = dimStyle.Render("  Yes  ")
	} else {
		noStyled = dimStyle.Render("  No  ")
		yesStyled = selStyle.Render("◀ Yes ▶")
	}
	footerRendered := ppText.Render("  ") + noStyled + ppText.Render("   ") + yesStyled
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
	// Row 0 is the control bar, rows [1 .. convHeight] are the conversation, and
	// the input box starts right after at row convHeight+1. We want the popup to
	// end at row convHeight, flush against the input box's top border.
	inputBoxTopRow := m.convHeight() + 1
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

// controlBarBg is the background for the always-visible top control bar
// (k9s-style): auto-approve edits/shell and the model selector, editable
// without needing the agent to be idle — see focusAutoApproveEdits etc.
var (
	controlBarBg      = lipgloss.Color("#12202E")
	controlBarStyle   = lipgloss.NewStyle().Background(controlBarBg).Foreground(lipgloss.Color("#88AACC"))
	controlBarFocus   = lipgloss.NewStyle().Background(controlBarBg).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	controlBarHintSty = lipgloss.NewStyle().Background(controlBarBg).Foreground(lipgloss.Color("#556677"))
)

// renderControlBar renders the always-visible top bar: auto-approve edits,
// auto-approve shell, and model, each highlighted when focused. Edits/Shell
// are grouped under a shared "Auto-approve:" label — dropped first on a
// narrow terminal, then the Tab hint, falling back to the bare fields.
func (m Model) renderControlBar() string {
	edits, shell := m.prog.autoApprove()
	modelLabel := m.model
	if modelLabel == "" {
		modelLabel = "default"
	}

	styleFor := func(f inputFocus) lipgloss.Style {
		if m.focus == f {
			return controlBarFocus
		}
		return controlBarStyle
	}
	editsField := styleFor(focusAutoApproveEdits).Render(fmt.Sprintf(" Edits: %s ", onOff(edits)))
	shellField := styleFor(focusAutoApproveShell).Render(fmt.Sprintf(" Shell: %s ", onOff(shell)))
	modelField := styleFor(focusModel).Render(fmt.Sprintf(" Model: %s ", modelLabel))
	sep := controlBarStyle.Render(" ")
	groupSep := controlBarStyle.Render("   ") // wider gap: separates the auto-approve group from Model

	fieldsOnly := editsField + sep + shellField + groupSep + modelField
	withLabel := controlBarHintSty.Render(" Auto-approve: ") + editsField + sep + shellField + groupSep + modelField

	const hint = "Tab: cycle focus  ·  ←/→ change  ·  Space/Enter toggle "
	candidates := []struct{ left, right string }{
		{withLabel, hint},
		{withLabel, ""},
		{fieldsOnly, ""},
	}
	for _, c := range candidates {
		pad := m.width - lipgloss.Width(c.left) - lipgloss.Width(c.right)
		if pad >= 1 {
			return c.left + controlBarStyle.Render(strings.Repeat(" ", pad)) + controlBarHintSty.Render(c.right)
		}
	}
	return controlBarStyle.Width(m.width).Render(fieldsOnly)
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

	// Right side: context stats and the plan-limit warning (model and
	// auto-approve state live in the top control bar instead). Cost appears
	// only in API mode where tokens are billed directly. The plan warning
	// prefers its full text with reset times; on narrow terminals it
	// degrades to compact chips, then drops entirely rather than truncating
	// the label.
	var ctxSeg string
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
			lines = append(lines, m.renderMsg(i, msg, w)...)
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
	renderLine := func(text string, fg color.Color) string {
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

// mdCacheEntry is one memoized renderMarkdown result, keyed by conv index in
// Model.mdRenderCache and invalidated when content or width no longer match.
type mdCacheEntry struct {
	content string
	width   int
	lines   []string
}

func (m Model) renderMsg(convIdx int, msg ConvMsg, w int) []string {
	switch msg.Role {
	case RoleStatus:
		return []string{padRight(statusStyle.Render("  · "+msg.Content), w)}

	case RoleAssistant:
		// No label — just the markdown content, like a chat response.
		if e, ok := m.mdRenderCache[convIdx]; ok && e.content == msg.Content && e.width == w {
			return e.lines
		}
		lines := renderMarkdown(msg.Content, w)
		m.mdRenderCache[convIdx] = mdCacheEntry{content: msg.Content, width: w, lines: lines}
		return lines

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

	focused := m.focus == focusTextInput
	border := inputBorderSty
	promptSty := inputPromptDimSty
	textSty := inputTextDimSty
	if focused {
		border = inputBorderFocusSty
		promptSty = inputPromptSty
		textSty = lipgloss.NewStyle() // unstyled: default terminal foreground
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
	topBorder := border.Render("╭─" + hintStr + strings.Repeat("─", topDashes) + "╮")
	botBorder := border.Render("╰" + strings.Repeat("─", innerW) + "╯")

	prompt := promptSty.Render("▶ ")
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
			before := textSty.Render(string(lineRunes[:col]))
			var curChar, after string
			if col < len(lineRunes) {
				after = textSty.Render(string(lineRunes[col+1:]))
				if focused {
					curChar = cursorStyle.Render(string(lineRunes[col]))
				} else {
					curChar = textSty.Render(string(lineRunes[col]))
				}
			} else if focused {
				curChar = cursorStyle.Render(" ")
			}
			lineContent = before + curChar + after
		} else {
			lineContent = textSty.Render(string(lineRunes))
		}

		lineW := lipgloss.Width(prefix + lineContent)
		pad := strings.Repeat(" ", max(0, innerW-lineW))
		sb.WriteString(border.Render("│"))
		sb.WriteString(prefix + lineContent + pad)
		sb.WriteString(border.Render("│"))
	}

	// Pad remaining content rows with empty bordered lines.
	for i := winEnd - winStart; i < contentH; i++ {
		sb.WriteByte('\n')
		sb.WriteString(border.Render("│"))
		sb.WriteString(strings.Repeat(" ", innerW))
		sb.WriteString(border.Render("│"))
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
// Layout: bar(1) + '\n' + conv(convHeight) + '\n' + input(inputHeight) + '\n' + header(1)
// Total rows = 1 + convHeight + inputHeight + 1 (bar and header are each a
// single row; no separate divider rows).
func (m Model) convHeight() int {
	return max(1, m.height-m.inputHeight()-2)
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
	p := tea.NewProgram(model, tea.WithoutSignalHandler())
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

// adaptiveDark reports whether the terminal background is dark. lipgloss v2
// dropped AdaptiveColor (a value that resolved itself lazily against a
// globally-detected background) in favour of LightDark(isDark), which
// resolves explicitly — so the background has to be known here rather than
// discovered inside lipgloss.
//
// Defaults to dark, matching indigo's own default-dark theme, and is
// corrected at startup: Init requests the terminal background and Update
// handles BackgroundColorMsg, rebuilding the palette via
// refreshAdaptiveColors if the terminal turns out to be light.
var adaptiveDark = true

// adaptive picks between a dark- and light-background colour, replacing
// lipgloss v1's AdaptiveColor{Dark:..., Light:...} literals.
func adaptive(dark, light string) color.Color {
	return lipgloss.LightDark(adaptiveDark)(lipgloss.Color(light), lipgloss.Color(dark))
}

// refreshAdaptiveColors recomputes every adaptive palette value for the
// current adaptiveDark. adaptive() resolves eagerly — it returns a concrete
// color, not something that re-reads adaptiveDark later — so the package
// vars built at init time hold dark-theme colors until this runs.
//
// Called when the terminal answers RequestBackgroundColor (see Init), which
// is the v2 replacement for lipgloss v1's AdaptiveColor, where the library
// did the terminal query itself.
func refreshAdaptiveColors() {
	bubbleBg = adaptive("#1B2939", "#DDE8F4")
	bubbleFg = adaptive("#C8D8E8", "#2A3A4A")
	bubbleDim = []color.Color{
		adaptive("#C8D8E8", "#2A3A4A"),
		adaptive("#8898A8", "#627282"),
		adaptive("#4A5A6A", "#96A6B6"),
	}
	bubbleBorderColor = adaptive("#3D5472", "#7A9DC0")
	bubbleHintColor = adaptive("#445566", "#8899AA")
}
