package client

import (
	"context"
	"fmt"
	"strings"
	"time"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/twist/internal/document"
)

type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert
)

// tickMsg is sent periodically to poll for remote updates.
type tickMsg struct{}

// updatesMsg carries ops received from the server.
type updatesMsg struct {
	ops     []document.Op
	version uint64
}

// errorMsg carries a non-fatal error to display in the status bar.
type errorMsg struct{ err error }

// savedMsg signals a successful save.
type savedMsg struct{}

// clientCountMsg carries the result of a bufferClientCount RPC.
type clientCountMsg struct{ count uint32 }

// saveAndQuitMsg triggers a quit after a save has completed.
type saveAndQuitMsg struct{}

var (
	barStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("#087AC8")).
		Foreground(lipgloss.Color("#FFFFFF"))

	normalModeStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#087AC8")).
			Foreground(lipgloss.Color("#AAFFAA")).
			Bold(true)

	insertModeStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#087AC8")).
			Foreground(lipgloss.Color("#AADDFF")).
			Bold(true)

	cursorStyle = lipgloss.NewStyle().Reverse(true)
)

// Model is the Bubble Tea model for a single buffer view.
type Model struct {
	rpc          *RPC
	buf          *document.Buffer
	bufID        uint32
	version      uint64
	mode         Mode
	cursor       document.Pos
	topLine      int // first visible line
	width        int
	height       int
	filePath     string
	status       string // transient error message shown in modeline
	quitting     bool
	warnQuit     bool // showing unsaved-changes warning
	checkingQuit bool // client-count RPC in flight
}

// New creates a Model after the buffer is already open with the server.
func New(rpc *RPC, bufID uint32, content string, version uint64, filePath string) Model {
	return Model{
		rpc:      rpc,
		buf:      document.New(filePath, content),
		bufID:    bufID,
		version:  version,
		filePath: filePath,
	}
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Init() tea.Cmd {
	return tick()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.fetchUpdates(), tick())

	case updatesMsg:
		for _, op := range msg.ops {
			m.buf.Apply(op)
		}
		m.version = msg.version
		m.clampCursor()
		return m, nil

	case errorMsg:
		m.status = "ERR: " + msg.err.Error()
		return m, nil

	case savedMsg:
		m.buf.SetClean()
		m.status = ""
		return m, nil

	case clientCountMsg:
		m.checkingQuit = false
		if msg.count <= 1 {
			m.warnQuit = true
		} else {
			m.quitting = true
			return m, tea.Sequence(m.doDisconnect(), tea.Quit)
		}
		return m, nil

	case saveAndQuitMsg:
		m.buf.SetClean()
		m.quitting = true
		return m, tea.Sequence(m.doDisconnect(), tea.Quit)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.warnQuit {
		return m.handleWarnQuit(msg)
	}
	// Clear transient error on any key.
	m.status = ""
	if m.mode == ModeNormal {
		return m.handleNormal(msg)
	}
	return m.handleInsert(msg)
}

func (m Model) handleWarnQuit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "s", "ctrl+s":
		m.warnQuit = false
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m.rpc.Save(ctx, m.bufID)
			return saveAndQuitMsg{}
		}
	case "q", "Q":
		m.warnQuit = false
		m.quitting = true
		return m, tea.Sequence(m.doDisconnect(), tea.Quit)
	case "esc", "n", "N":
		m.warnQuit = false
	}
	return m, nil
}

func (m Model) handleNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		if m.buf.Dirty() && !m.checkingQuit {
			m.checkingQuit = true
			return m, m.fetchClientCount()
		}
		if !m.checkingQuit {
			m.quitting = true
			return m, tea.Sequence(m.doDisconnect(), tea.Quit)
		}

	case "i":
		m.mode = ModeInsert
		m.status = ""

	case "a":
		// Insert after cursor
		m.mode = ModeInsert
		lineLen := m.buf.LineLen(m.cursor.Line)
		if m.cursor.Col < lineLen {
			m.cursor.Col++
		}
		m.status = ""

	case "o":
		// Open new line below
		m.mode = ModeInsert
		line := m.cursor.Line
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: line,
			InsertCol:  m.buf.LineLen(line),
			InsertText: "\n",
		}
		return m, m.sendOp(op)

	case "O":
		// Open new line above
		m.mode = ModeInsert
		col := 0
		if m.cursor.Line > 0 {
			col = m.buf.LineLen(m.cursor.Line - 1)
		}
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: max(0, m.cursor.Line-1),
			InsertCol:  col,
			InsertText: "\n",
		}
		return m, m.sendOp(op)

	case "s", "ctrl+s":
		return m, m.doSave()

	// Movement
	case "h", "left":
		m.moveCursor(0, -1)
	case "l", "right":
		m.moveCursor(0, 1)
	case "j", "down":
		m.moveCursor(1, 0)
	case "k", "up":
		m.moveCursor(-1, 0)
	case "ctrl+f", "pgdown":
		m.moveCursor(m.visibleLines(), 0)
	case "ctrl+b", "pgup":
		m.moveCursor(-m.visibleLines(), 0)
	case "g":
		m.cursor = document.Pos{Line: 0, Col: 0}
		m.topLine = 0
	case "G":
		last := max(0, m.buf.LineCount()-1)
		m.cursor = document.Pos{Line: last, Col: 0}
		m.scrollToCursor()
	case "0", "home":
		m.cursor.Col = 0
	case "$", "end":
		m.cursor.Col = max(0, m.buf.LineLen(m.cursor.Line)-1)

	// Delete character under cursor (x)
	case "x":
		lineLen := m.buf.LineLen(m.cursor.Line)
		if lineLen > 0 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col + 1,
			}
			return m, m.sendOp(op)
		}

	// Delete line (dd)
	case "d":
		// Simplified: delete to end of line
		lineLen := m.buf.LineLen(m.cursor.Line)
		if lineLen > 0 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  0,
				ToLine:   m.cursor.Line,
				ToCol:    lineLen,
			}
			return m, m.sendOp(op)
		}
	}
	return m, nil
}

func (m Model) handleInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		// Move cursor left one if not at col 0 (Vim/Helix convention)
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		return m, nil

	case "ctrl+c":
		m.quitting = true
		return m, tea.Sequence(m.doDisconnect(), tea.Quit)

	case "ctrl+s":
		return m, m.doSave()

	case "backspace":
		if m.cursor.Col > 0 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col - 1,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col,
			}
			m.cursor.Col--
			return m, m.sendOp(op)
		} else if m.cursor.Line > 0 {
			// Merge with previous line
			prevLen := m.buf.LineLen(m.cursor.Line - 1)
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line - 1,
				FromCol:  prevLen,
				ToLine:   m.cursor.Line,
				ToCol:    0,
			}
			m.cursor = document.Pos{Line: m.cursor.Line - 1, Col: prevLen}
			return m, m.sendOp(op)
		}

	case "delete":
		lineLen := m.buf.LineLen(m.cursor.Line)
		if m.cursor.Col < lineLen {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  m.cursor.Col,
				ToLine:   m.cursor.Line,
				ToCol:    m.cursor.Col + 1,
			}
			return m, m.sendOp(op)
		} else if m.cursor.Line < m.buf.LineCount()-1 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  lineLen,
				ToLine:   m.cursor.Line + 1,
				ToCol:    0,
			}
			return m, m.sendOp(op)
		}

	case "enter":
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: m.cursor.Line + 1, Col: 0}
		return m, m.sendOp(op)

	case "tab":
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\t",
		}
		m.cursor.Col++
		return m, m.sendOp(op)

	case "left":
		m.moveCursor(0, -1)
	case "right":
		m.moveCursor(0, 1)
	case "up":
		m.moveCursor(-1, 0)
	case "down":
		m.moveCursor(1, 0)
	case "home":
		m.cursor.Col = 0
	case "end":
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	default:
		// Printable rune(s)
		if len(msg.Runes) > 0 {
			text := string(msg.Runes)
			op := document.Op{
				ClientID:   m.rpc.ClientID(),
				Type:       document.OpInsert,
				InsertLine: m.cursor.Line,
				InsertCol:  m.cursor.Col,
				InsertText: text,
			}
			m.cursor.Col += len(msg.Runes)
			return m, m.sendOp(op)
		}
	}
	return m, nil
}

// sendOp applies the op locally and sends it to the server.
func (m Model) sendOp(op document.Op) tea.Cmd {
	m.buf.Apply(op)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ver, err := m.rpc.ApplyOp(ctx, m.bufID, op)
		if err != nil {
			return errorMsg{err}
		}
		m.version = ver
		return nil
	}
}

func (m Model) fetchUpdates() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		ops, ver, err := m.rpc.GetUpdates(ctx, m.bufID, m.version)
		if err != nil || len(ops) == 0 {
			return nil
		}
		return updatesMsg{ops: ops, version: ver}
	}
}

func (m Model) doSave() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.rpc.Save(ctx, m.bufID); err != nil {
			return errorMsg{err}
		}
		return savedMsg{}
	}
}

func (m Model) fetchClientCount() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		count, err := m.rpc.BufferClientCount(ctx, m.bufID)
		if err != nil {
			// On error, assume we're alone and warn.
			return clientCountMsg{count: 1}
		}
		return clientCountMsg{count: count}
	}
}

func (m Model) doDisconnect() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.CloseBuffer(ctx, m.bufID)
		m.rpc.Disconnect(ctx)
		return nil
	}
}

// ---- cursor movement ----

func (m *Model) moveCursor(dLine, dCol int) {
	line := m.cursor.Line + dLine
	if line < 0 {
		line = 0
	}
	if line >= m.buf.LineCount() {
		line = max(0, m.buf.LineCount()-1)
	}
	col := m.cursor.Col + dCol
	lineLen := m.buf.LineLen(line)
	maxCol := lineLen
	if m.mode == ModeNormal && maxCol > 0 {
		maxCol-- // in normal mode cursor sits on a character
	}
	if col < 0 {
		col = 0
	}
	if col > maxCol {
		col = maxCol
	}
	m.cursor = document.Pos{Line: line, Col: col}
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	line := m.cursor.Line
	if line >= m.buf.LineCount() {
		line = max(0, m.buf.LineCount()-1)
	}
	col := m.cursor.Col
	if col > m.buf.LineLen(line) {
		col = m.buf.LineLen(line)
	}
	m.cursor = document.Pos{Line: line, Col: col}
	m.scrollToCursor()
}

func (m *Model) scrollToCursor() {
	vis := m.visibleLines()
	if m.cursor.Line < m.topLine {
		m.topLine = m.cursor.Line
	}
	if m.cursor.Line >= m.topLine+vis {
		m.topLine = m.cursor.Line - vis + 1
	}
	if m.topLine < 0 {
		m.topLine = 0
	}
}

func (m Model) visibleLines() int {
	h := m.height - 1 // reserve one line for status bar
	if h < 1 {
		return 1
	}
	return h
}

// ---- View ----

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "loading…"
	}

	vis := m.visibleLines()
	lineCount := m.buf.LineCount()
	var sb strings.Builder

	for i := 0; i < vis; i++ {
		lineNum := m.topLine + i
		if lineNum >= lineCount {
			sb.WriteString("~\n")
			continue
		}

		line := m.buf.Line(lineNum)
		runes := []rune(line)

		if lineNum == m.cursor.Line {
			// Render line with cursor highlight.
			curCol := m.cursor.Col
			if curCol > len(runes) {
				curCol = len(runes)
			}
			sb.WriteString(string(runes[:curCol]))
			if curCol < len(runes) {
				sb.WriteString(cursorStyle.Render(string(runes[curCol : curCol+1])))
				sb.WriteString(string(runes[curCol+1:]))
			} else {
				// Cursor at end of line: show a space.
				sb.WriteString(cursorStyle.Render(" "))
			}
		} else {
			sb.WriteString(line)
		}
		sb.WriteByte('\n')
	}

	sb.WriteString(m.renderStatusBar())

	return sb.String()
}

func (m Model) renderStatusBar() string {
	if m.width == 0 {
		return ""
	}

	// Left: mode indicator.
	modeLabel := "NORMAL"
	ms := normalModeStyle
	if m.mode == ModeInsert {
		modeLabel = "INSERT"
		ms = insertModeStyle
	}
	left := ms.Render("  " + modeLabel + "  ")
	leftW := lipgloss.Width(left)

	// Right: line:col position.
	posStr := fmt.Sprintf("  %d:%d  ", m.cursor.Line+1, m.cursor.Col+1)
	right := barStyle.Render(posStr)
	rightW := lipgloss.Width(right)

	// Center: file path (+ dirty marker), warn message, or error.
	var centerContent string
	switch {
	case m.warnQuit:
		centerContent = "Unsaved changes!   Save [s]   Discard [q]   Cancel [esc]"
	case m.status != "":
		centerContent = m.filePath + "   " + m.status
		if m.buf.Dirty() {
			centerContent = m.filePath + " [+]   " + m.status
		}
	default:
		centerContent = m.filePath
		if m.buf.Dirty() {
			centerContent += " [+]"
		}
	}

	centerW := m.width - leftW - rightW
	if centerW < 0 {
		centerW = 0
	}
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
