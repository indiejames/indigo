package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/server"
)

// ─── message types ───────────────────────────────────────────────────────────

// MsgRole classifies a conversation entry. Adding new roles here (e.g.
// RoleDiff, RolePermission) is the extension point for future features.
type MsgRole int

const (
	RoleStatus    MsgRole = iota // system/context updates, shown dimmed
	RoleUser                     // user input
	RoleAssistant                // Claude response
)

// ConvMsg is one entry in the conversation history.
// Keep it simple for now; future roles (diff, permission) will add fields.
type ConvMsg struct {
	Role    MsgRole
	Content string
	// Pending bool  // future: true while streaming
}

// ─── tea messages ────────────────────────────────────────────────────────────

type tickMsg struct{}
type activeCtxUpdated struct{ ctx client.ActiveContext }

// ─── model ───────────────────────────────────────────────────────────────────

type Model struct {
	rpc       *client.RPC
	width     int
	height    int
	conv      []ConvMsg
	input     []rune
	inputPos  int // cursor position within input (rune index)
	activeCtx client.ActiveContext
	scroll    int  // lines scrolled up from bottom; 0 = show latest
	ready     bool // false until first WindowSizeMsg
}

func newModel(rpc *client.RPC) Model {
	return Model{
		rpc:  rpc,
		conv: []ConvMsg{{Role: RoleStatus, Content: "Connected to indigo server."}},
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
			m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Active file: " + m.displayPath(msg.ctx.FilePath)})
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.conv = append(m.conv, ConvMsg{Role: RoleStatus, Content: "Press ctrl+q to quit."})
		return m, nil

	case tea.KeyCtrlQ:
		return m, tea.Quit

	case tea.KeyEnter:
		text := strings.TrimSpace(string(m.input))
		if text == "" {
			return m, nil
		}
		m.conv = append(m.conv, ConvMsg{Role: RoleUser, Content: text})
		m.input = nil
		m.inputPos = 0
		m.scroll = 0
		// TODO: wire Claude API here; for now echo a placeholder
		m.conv = append(m.conv, ConvMsg{
			Role:    RoleAssistant,
			Content: "(Claude API not yet connected — coming soon)",
		})
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		if m.inputPos > 0 {
			m.input = append(m.input[:m.inputPos-1], m.input[m.inputPos:]...)
			m.inputPos--
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

	case tea.KeyPgUp:
		m.scroll += m.convHeight() / 2
		return m, nil

	case tea.KeyPgDown:
		m.scroll -= m.convHeight() / 2
		if m.scroll < 0 {
			m.scroll = 0
		}
		return m, nil

	case tea.KeyRunes:
		r := []rune(msg.String())
		m.input = append(m.input[:m.inputPos], append(r, m.input[m.inputPos:]...)...)
		m.inputPos += len(r)
		return m, nil
	}
	return m, nil
}

// ─── view ────────────────────────────────────────────────────────────────────

var (
	headerStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#087AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	youStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	claudeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#44DDAA")).Bold(true)
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#667788"))
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
	return sb.String()
}

func (m Model) renderHeader() string {
	var label string
	if m.activeCtx.Found {
		label = fmt.Sprintf(" indigo-claude  ·  %s  line %d",
			m.displayPath(m.activeCtx.FilePath),
			m.activeCtx.Line+1)
	} else {
		label = " indigo-claude"
	}
	return headerStyle.Width(m.width).Render(label)
}

func (m Model) renderConversation() string {
	h := m.convHeight()
	if h <= 0 {
		return ""
	}

	// Render all messages to visual lines.
	lines := m.renderAllLines()

	// Apply scroll offset (clamped so we can't scroll past top).
	maxScroll := len(lines) - h
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := m.scroll
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Select the window of lines to display (bottom-anchored by default).
	start := len(lines) - h - scroll
	if start < 0 {
		start = 0
	}
	end := start + h
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[start:end]

	// Pad with empty lines at top if conversation is short.
	var rows strings.Builder
	for i := 0; i < h-len(visible); i++ {
		rows.WriteString(strings.Repeat(" ", m.width))
		rows.WriteByte('\n')
	}
	for i, l := range visible {
		rows.WriteString(l)
		if i < len(visible)-1 {
			rows.WriteByte('\n')
		}
	}
	return rows.String()
}

// renderAllLines converts m.conv into a flat list of terminal-width strings.
func (m Model) renderAllLines() []string {
	w := m.width
	if w < 8 {
		w = 8
	}
	var lines []string
	for i, msg := range m.conv {
		if i > 0 {
			lines = append(lines, "") // blank separator between messages
		}
		lines = append(lines, m.renderMsg(msg, w)...)
	}
	return lines
}

// renderMsg renders one ConvMsg to visual lines of exactly width w.
func (m Model) renderMsg(msg ConvMsg, w int) []string {
	switch msg.Role {
	case RoleStatus:
		text := statusStyle.Render("  · " + msg.Content)
		return []string{padRight(text, w)}

	case RoleUser:
		label := youStyle.Render("You")
		sep := dividerStyle.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label)-2)))
		header := label + " " + sep
		var out []string
		out = append(out, header)
		for _, l := range wordWrap(msg.Content, w-2) {
			out = append(out, "  "+l)
		}
		return out

	case RoleAssistant:
		label := claudeStyle.Render("Claude")
		sep := dividerStyle.Render(strings.Repeat("─", max(0, w-lipgloss.Width(label)-2)))
		header := label + " " + sep
		var out []string
		out = append(out, header)
		for _, l := range wordWrap(msg.Content, w-2) {
			out = append(out, "  "+l)
		}
		return out
	}
	return nil
}

func (m Model) renderDivider() string {
	return dividerStyle.Render(strings.Repeat("─", m.width))
}

func (m Model) renderInput() string {
	prompt := inputPromptSty.Render("▶ ")
	pw := lipgloss.Width(prompt)
	avail := m.width - pw
	if avail < 1 {
		avail = 1
	}

	// Build the input display with a cursor block.
	before := string(m.input[:m.inputPos])
	after := string(m.input[m.inputPos:])

	// If the input is wider than available, scroll right so cursor is visible.
	if len([]rune(before)) > avail-1 {
		runes := []rune(before)
		before = string(runes[len(runes)-(avail-1):])
	}

	var cursorChar string
	if len(after) > 0 {
		cursorChar = cursorStyle.Render(string([]rune(after)[0]))
		if len([]rune(after)) > 1 {
			after = string([]rune(after)[1:])
		} else {
			after = ""
		}
	} else {
		cursorChar = cursorStyle.Render(" ")
		after = ""
	}

	// Truncate after-cursor text to available width.
	remainAfter := avail - len([]rune(before)) - 1
	if remainAfter < 0 {
		remainAfter = 0
	}
	afterRunes := []rune(after)
	if len(afterRunes) > remainAfter {
		afterRunes = afterRunes[:remainAfter]
	}

	return prompt + before + cursorChar + string(afterRunes)
}

// ─── layout helpers ──────────────────────────────────────────────────────────

func (m Model) convHeight() int {
	// header(1) + newline(1) + conv + newline(1) + divider(1) + newline(1) + input(1) = header+conv+4
	return max(1, m.height-5)
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

func (m Model) displayPath(filePath string) string {
	cwd, err := os.Getwd()
	if err == nil {
		if rel, err := filepath.Rel(cwd, filePath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return filepath.Base(filePath)
}

// wordWrap splits text into lines of at most width visible characters,
// breaking on word boundaries. Preserves existing newlines.
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

// padRight pads s to at least width terminal columns with spaces.
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

// acquireWorkspaceLock creates an exclusive flock on <socketDir>/claude.lock.
// The lock is released automatically when the process exits (even on crash).
// Returns the open file so the caller can keep it alive for the process lifetime.
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

	model := newModel(rpc)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithoutSignalHandler())
	rpc.SetPushSender(p.Send)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "indigo-claude: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rpc.Disconnect(ctx) //nolint:errcheck
}
