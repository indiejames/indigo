package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/server"
)

// ─── conversation message types ───────────────────────────────────────────────

type MsgRole int

const (
	RoleStatus     MsgRole = iota // system/context updates, shown dimmed
	RoleUser                      // user input
	RoleAssistant                 // Claude response
	RoleTool                      // tool call notification
	RolePermission                // pending edit approval (replaced by status once resolved)
)

type ConvMsg struct {
	Role    MsgRole
	Content string
}

// ─── tea messages ────────────────────────────────────────────────────────────

type tickMsg struct{}
type activeCtxUpdated struct{ ctx client.ActiveContext }

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

	// CLI mode state.
	sessionID string

	// streaming state (both modes)
	agentRunning     bool
	streamingConvIdx int

	activeCtx client.ActiveContext
	scroll    int
	ready     bool

	// Input history (most recent last); historyIdx -1 = not browsing.
	inputHistory []string
	historyIdx   int
	savedInput   string
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
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(scheduleTick(), m.cmdPollActiveCtx())
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
		return m, tea.Batch(scheduleTick(), m.cmdPollActiveCtx())

	case activeCtxUpdated:
		prev := m.activeCtx
		m.activeCtx = msg.ctx
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
		m.conv = append(m.conv, ConvMsg{Role: RoleTool, Content: "  ⟳ " + msg.name + "…"})
		return m, nil

	case agentToolDoneMsg:
		// Mark the most recent pending tool entry as done.
		for i := len(m.conv) - 1; i >= 0; i-- {
			if m.conv[i].Role == RoleTool && strings.HasSuffix(m.conv[i].Content, "…") {
				m.conv[i].Content = "  ✓ " + strings.TrimSuffix(strings.TrimPrefix(m.conv[i].Content, "  ⟳ "), "…")
				break
			}
		}
		// Reset streaming index so next assistant segment starts a new ConvMsg.
		m.streamingConvIdx = -1
		return m, nil

	case permissionRequestMsg:
		m.pendingPerm = &msg
		m.conv = append(m.conv, ConvMsg{Role: RolePermission, Content: renderPermissionDiff(msg)})
		m.scroll = 0
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
		return m, nil

	case agentErrorMsg:
		m.agentRunning = false
		m.streamingConvIdx = -1
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Error: " + msg.err.Error()})
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingPerm != nil {
		return m.handlePermissionKey(msg)
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

		// Slash commands.
		if text == "/clear" {
			m.conv = []ConvMsg{{Role: RoleStatus, Content: "Conversation cleared."}}
			m.history = nil
			m.sessionID = ""
			m.input = nil
			m.inputPos = 0
			m.scroll = 0
			m.historyIdx = -1
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

		// Save to history (skip consecutive duplicates).
		if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
			m.inputHistory = append(m.inputHistory, text)
		}
		m.historyIdx = -1
		m.savedInput = ""

		m.conv = append(m.conv, ConvMsg{Role: RoleUser, Content: text})
		m.input = nil
		m.inputPos = 0
		m.scroll = 0
		m.streamingConvIdx = -1
		m.agentRunning = true

		prog := m.prog
		ac := m.activeCtx
		snippet := bufferSnippet(ac.FilePath, int(ac.Line), 20)
		if m.apiKey != "" {
			history := append(m.history, buildUserMessage(text, ac, snippet)) //nolint:gocritic
			apiKey := m.apiKey
			rpc := m.rpc
			workDir := m.workDir
			go runAgent(prog, rpc, apiKey, workDir, history, ac, snippet)
		} else {
			go runClaudeSubprocess(prog, m.workDir, text, m.sessionID, ac, snippet)
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
		m.inputPos = 0
		return m, nil

	case tea.KeyEnd, tea.KeyCtrlE:
		m.inputPos = len(m.input)
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
		m.scroll++
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
		if m.scroll > 0 {
			m.scroll--
		}
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
		r := []rune(msg.String())
		m.input = append(m.input[:m.inputPos], append(r, m.input[m.inputPos:]...)...)
		m.inputPos += len(r)
		m.historyIdx = -1
		return m, nil
	}
	return m, nil
}

func (m Model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	perm := m.pendingPerm
	m.pendingPerm = nil

	approved := msg.String() == "y" || msg.String() == "Y"
	label := "Rejected: " + perm.file
	if approved {
		label = "Approved: " + perm.file
	}
	for i := len(m.conv) - 1; i >= 0; i-- {
		if m.conv[i].Role == RolePermission {
			m.conv[i] = ConvMsg{Role: RoleStatus, Content: label}
			break
		}
	}
	perm.replyCh <- approved
	return m, nil
}

func renderPermissionDiff(perm permissionRequestMsg) string {
	var sb strings.Builder
	sb.WriteString("  File: " + perm.file + "\n")
	if perm.reason != "" {
		sb.WriteString("  Reason: " + perm.reason + "\n")
	}
	for _, e := range perm.edits {
		sb.WriteString("  ── remove ──\n")
		for _, l := range strings.Split(e.oldText, "\n") {
			sb.WriteString("  - " + l + "\n")
		}
		sb.WriteString("  ── add ──\n")
		for _, l := range strings.Split(e.newText, "\n") {
			sb.WriteString("  + " + l + "\n")
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ─── view ────────────────────────────────────────────────────────────────────

var (
	headerStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#087AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	youStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	claudeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#44DDAA")).Bold(true)
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#667788"))
	toolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#AABBCC"))
	permStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8844")).Bold(true)
	diffOldStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	diffNewStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#55FF55"))
	dividerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#334455"))
	inputPromptSty = lipgloss.NewStyle().Foreground(lipgloss.Color("#087AC8")).Bold(true)
	cursorStyle    = lipgloss.NewStyle().Reverse(true)
)

func (m Model) View() string {
	if !m.ready {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(m.renderHeader())
	sb.WriteByte('\n')
	sb.WriteString(m.renderConversation())
	sb.WriteByte('\n')
	sb.WriteString(m.renderDivider())
	sb.WriteByte('\n')
	sb.WriteString(m.renderInput())
	// Pad remaining input rows with blank lines so the layout never shifts when
	// inputLineCount changes — Bubble Tea's delta renderer would otherwise leave
	// stale content from the old larger input area on screen.
	for i := m.inputLineCount(); i < maxInputLines; i++ {
		sb.WriteByte('\n')
		sb.WriteString(strings.Repeat(" ", m.width))
	}
	return sb.String()
}

func (m Model) renderHeader() string {
	label := " indigo-claude"
	if m.activeCtx.Found {
		label = fmt.Sprintf(" indigo-claude  ·  %s  line %d",
			m.displayPath(m.activeCtx.FilePath), m.activeCtx.Line+1)
	}
	if m.pendingPerm != nil {
		label += "  [approve edit? y/n]"
	} else if m.agentRunning {
		label += "  [thinking…]"
	}
	return headerStyle.Width(m.width).Render(label)
}

func (m Model) renderConversation() string {
	h := m.convHeight()
	if h <= 0 {
		return ""
	}
	lines := m.renderAllLines()

	maxScroll := len(lines) - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.scroll
	if scroll > maxScroll {
		scroll = maxScroll
	}
	start := len(lines) - h - scroll
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[start:end]

	var rows strings.Builder
	for i := 0; i < h-len(visible); i++ {
		rows.WriteString(strings.Repeat(" ", m.width))
		rows.WriteByte('\n')
	}
	for i, l := range visible {
		rows.WriteString(padRight(strings.TrimRight(l, "\r"), m.width))
		if i < len(visible)-1 {
			rows.WriteByte('\n')
		}
	}
	return rows.String()
}

func (m Model) renderAllLines() []string {
	w := m.width
	if w < 8 {
		w = 8
	}
	var lines []string
	for i, msg := range m.conv {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, m.renderMsg(msg, w)...)
	}
	return lines
}

func (m Model) renderMsg(msg ConvMsg, w int) []string {
	switch msg.Role {
	case RoleStatus:
		return []string{padRight(statusStyle.Render("  · "+msg.Content), w)}

	case RoleUser:
		label := youStyle.Render("You")
		sep := dividerStyle.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label)-2)))
		var out []string
		out = append(out, label+" "+sep)
		for _, l := range wordWrap(msg.Content, w-2) {
			out = append(out, "  "+l)
		}
		return out

	case RoleAssistant:
		label := claudeStyle.Render("Claude")
		sep := dividerStyle.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label)-2)))
		var out []string
		out = append(out, label+" "+sep)
		out = append(out, renderMarkdown(msg.Content, w)...)
		return out

	case RoleTool:
		return []string{padRight(toolStyle.Render(msg.Content), w)}

	case RolePermission:
		header := permStyle.Render("  ⚠ Edit request — approve? [y]es / [n]o")
		out := []string{padRight(header, w)}
		for _, l := range strings.Split(msg.Content, "\n") {
			// Colour the diff lines.
			switch {
			case strings.HasPrefix(l, "  - "):
				out = append(out, diffOldStyle.Render(l))
			case strings.HasPrefix(l, "  + "):
				out = append(out, diffNewStyle.Render(l))
			default:
				out = append(out, statusStyle.Render(l))
			}
		}
		return out
	}
	return nil
}

func (m Model) renderDivider() string {
	var hint string
	switch {
	case m.agentRunning:
		hint = statusStyle.Render(" thinking…")
	case m.scroll > 0:
		hint = statusStyle.Render(" ↑↓ / PgUp PgDn to scroll · ↓ for latest")
	}
	dashes := max(0, m.width-lipgloss.Width(hint))
	return dividerStyle.Render(strings.Repeat("─", dashes)) + hint
}

func (m Model) renderInput() string {
	prompt := inputPromptSty.Render("▶ ")
	pw := lipgloss.Width(prompt)
	cont := strings.Repeat(" ", pw) // continuation prefix for lines 2+
	avail := m.width - pw
	if avail < 1 {
		avail = 1
	}

	inputText := string(m.input)
	inputLines := strings.Split(inputText, "\n")

	// Find which line and column within that line the cursor sits on.
	beforeCursor := string(m.input[:m.inputPos])
	beforeLines := strings.Split(beforeCursor, "\n")
	cursorLine := len(beforeLines) - 1
	cursorCol := len([]rune(beforeLines[cursorLine]))

	// Show a window of maxInputLines lines centred on the cursor line.
	totalLines := len(inputLines)
	winStart := max(0, cursorLine-maxInputLines+1)
	winEnd := min(totalLines, winStart+maxInputLines)

	var sb strings.Builder
	for i := winStart; i < winEnd; i++ {
		if i > winStart {
			sb.WriteByte('\n')
		}
		if i == 0 {
			sb.WriteString(prompt)
		} else {
			sb.WriteString(cont)
		}

		lineRunes := []rune(inputLines[i])

		if i == cursorLine {
			col := cursorCol
			// Scroll the view rightward if cursor is past the visible area.
			viewStart := 0
			if col >= avail {
				viewStart = col - avail + 1
			}
			if viewStart > 0 {
				lineRunes = lineRunes[viewStart:]
				col -= viewStart
			}
			// Clamp rendered runes to avail width.
			if len(lineRunes) > avail {
				lineRunes = lineRunes[:avail]
			}

			before := string(lineRunes[:col])
			var curChar, after string
			if col < len(lineRunes) {
				curChar = cursorStyle.Render(string(lineRunes[col]))
				remaining := avail - col - 1
				tail := lineRunes[col+1:]
				if len(tail) > remaining {
					tail = tail[:remaining]
				}
				after = string(tail)
			} else {
				curChar = cursorStyle.Render(" ")
			}
			sb.WriteString(before + curChar + after)
		} else {
			if len(lineRunes) > avail {
				lineRunes = lineRunes[:avail]
			}
			sb.WriteString(string(lineRunes))
		}
	}
	return sb.String()
}

// ─── layout helpers ──────────────────────────────────────────────────────────

// maxInputLines is the maximum (and reserved) height of the input area.
// The conversation area is always m.height - 4 - maxInputLines rows so the
// layout never shifts when the input grows or shrinks (which would leave stale
// Bubble Tea delta-renderer content on screen).
const maxInputLines = 3

func (m Model) inputLineCount() int {
	n := strings.Count(string(m.input), "\n") + 1
	if n > maxInputLines {
		n = maxInputLines
	}
	return n
}

func (m Model) convHeight() int {
	return max(1, m.height-4-maxInputLines)
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
		return activeCtxUpdated{ctx: ac}
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
	model := newModel(rpc, prog, apiKey, workDir)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())
	rpc.SetPushSender(p.Send)

	prog.mu.Lock()
	prog.send = p.Send
	prog.mu.Unlock()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rpc.Disconnect(ctx) //nolint:errcheck
}
