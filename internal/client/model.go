package client

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/indiejames/twist/internal/config"
	"github.com/indiejames/twist/internal/document"
	"github.com/indiejames/twist/internal/highlight"
)

type Mode int

const (
	ModeNormal Mode = iota
	ModeInsert
	ModeCommand
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

// highlightMsg carries freshly computed syntax-highlight spans and parse time.
type highlightMsg struct {
	spans    highlight.LineSpans
	duration time.Duration
}

// metricsData holds timing samples for the metrics overlay.
// Stored behind a pointer so View()'s value receiver can write back.
type metricsData struct {
	show               bool
	lastKeyAt          time.Time
	renderDuration     time.Duration
	highlightDuration  time.Duration
	keyToFrameDuration time.Duration
}

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

	popupBg          = lipgloss.Color("#1E2A38")
	popupBorderStyle = lipgloss.NewStyle().
				Background(popupBg).
				Foreground(lipgloss.Color("#4488CC"))
	popupKeyStyle = lipgloss.NewStyle().
			Background(popupBg).
			Foreground(lipgloss.Color("#FFDD44")).
			Bold(true)
	popupTextStyle = lipgloss.NewStyle().
			Background(popupBg).
			Foreground(lipgloss.Color("#CCDDEE"))

	selectionStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#2D5F8A")).
			Foreground(lipgloss.Color("#FFFFFF"))

	gutterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#606060"))

	gutterCurStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#AAAAAA"))
)

const tabWidth = 4

// Selection tracks the selected range in the buffer.
// [Anchor, Head] is inclusive on both ends for display purposes.
// IsLine marks a linewise selection (x) which deletes the entire line + newline.
type Selection struct {
	Anchor document.Pos
	Head   document.Pos
	IsLine bool
}

// ordered returns (start, end) in document order (start <= end).
func (s *Selection) ordered() (start, end document.Pos) {
	if s.Anchor.Line < s.Head.Line ||
		(s.Anchor.Line == s.Head.Line && s.Anchor.Col <= s.Head.Col) {
		return s.Anchor, s.Head
	}
	return s.Head, s.Anchor
}

// command is a node in the prefix-command tree.
// Leaf nodes have execute set; branch nodes have children.
type command struct {
	key       rune
	label     string
	menuTitle string
	children  []command
	execute   func(m Model) (tea.Model, tea.Cmd)
}

// prefixCmds is the root of the prefix-command tree for Normal mode.
var prefixCmds = []command{
	{
		key:       'm',
		label:     "Match",
		menuTitle: "Match",
		children: []command{
			{key: 'm', label: "Go to matching bracket"},
			{
				key:       'i',
				label:     "Select inside object",
				menuTitle: "Match Inside",
				children: []command{
					{key: 'w', label: "Word", execute: executeSelectInsideWord},
					{key: 'm', label: "Closes surrounding pair"},
					{key: '.', label: "... or any character acting as a pair"},
				},
			},
		},
	},
}

// findCommand navigates prefixCmds following seq and returns the final node.
func findCommand(seq []rune) (*command, bool) {
	cmds := prefixCmds
	var found *command
	for _, r := range seq {
		matched := false
		for i := range cmds {
			if cmds[i].key == r {
				found = &cmds[i]
				cmds = cmds[i].children
				matched = true
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return found, found != nil
}

// executeSelectInsideWord selects the full word enclosing the cursor.
func executeSelectInsideWord(m Model) (tea.Model, tea.Cmd) {
	runes := []rune(m.buf.Line(m.cursor.Line))
	if len(runes) == 0 {
		return m, nil
	}
	col := min(m.cursor.Col, len(runes)-1)
	if !isWordChar(runes[col]) {
		return m, nil
	}
	start := col
	for start > 0 && isWordChar(runes[start-1]) {
		start--
	}
	end := col
	for end+1 < len(runes) && isWordChar(runes[end+1]) {
		end++
	}
	lineNum := m.cursor.Line
	m.sel = &Selection{
		Anchor: document.Pos{Line: lineNum, Col: start},
		Head:   document.Pos{Line: lineNum, Col: end},
	}
	m.cursor = m.sel.Head
	m.scrollToCursor()
	return m, nil
}

// Model is the Bubble Tea model for a single buffer view.
type Model struct {
	rpc            *RPC
	buf            *document.Buffer
	cfg            *config.Config
	bufID          uint32
	version        uint64
	mode           Mode
	cursor         document.Pos
	topLine        int // first visible line
	width          int
	height         int
	filePath       string
	status         string // transient error message shown in modeline
	quitting       bool
	warnQuit       bool // showing unsaved-changes warning
	checkingQuit   bool // client-count RPC in flight
	sel            *Selection
	dragging       bool
	undoStack      [][]document.Op // each entry is a group of inverse ops applied in reverse
	redoStack      [][]document.Op // mirrors undoStack; cleared on any new edit
	currentGroup   []document.Op   // non-nil while accumulating ops for the current Insert session
	savedUndoDepth int             // len(undoStack) at the time of the last save
	cmdBuf         string          // text typed after ':' while in ModeCommand
	prefixSeq      []rune          // keys typed so far for a multi-key Normal-mode command
	hlr            *highlight.Highlighter
	hlSpans        highlight.LineSpans
	metrics        *metricsData
}

// New creates a Model after the buffer is already open with the server.
func New(rpc *RPC, bufID uint32, content string, version uint64, filePath string, cfg *config.Config) Model {
	return Model{
		rpc:      rpc,
		buf:      document.New(filePath, content),
		cfg:      cfg,
		bufID:    bufID,
		version:  version,
		filePath: filePath,
		hlr:      highlight.New(filePath),
		metrics:  &metricsData{},
	}
}

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(tick(), m.reparseHighlight())
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
		return m, m.reparseHighlight()

	case errorMsg:
		m.status = "ERR: " + msg.err.Error()
		return m, nil

	case savedMsg:
		m.buf.SetClean()
		m.status = ""
		m.savedUndoDepth = len(m.undoStack)
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

	case highlightMsg:
		m.hlSpans = msg.spans
		if m.metrics != nil {
			m.metrics.highlightDuration = msg.duration
		}
		return m, nil

	case tea.MouseMsg:
		switch {
		case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
			m.handleMousePress(msg.X, msg.Y)
		case msg.Action == tea.MouseActionMotion && m.dragging:
			m.handleMouseDrag(msg.X, msg.Y)
		case msg.Action == tea.MouseActionRelease && msg.Button == tea.MouseButtonLeft:
			m.dragging = false
			// A press+release on the same spot is just a cursor move; clear selection.
			if m.sel != nil && m.sel.Anchor == m.sel.Head {
				m.sel = nil
			}
		}
		return m, nil

	case tea.KeyMsg:
		if m.metrics != nil {
			m.metrics.lastKeyAt = time.Now()
		}
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
	switch m.mode {
	case ModeNormal:
		return m.handleNormal(msg)
	case ModeInsert:
		return m.handleInsert(msg)
	case ModeCommand:
		return m.handleCommand(msg)
	}
	return m, nil
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
	// Handle active prefix sequence before the main switch.
	if len(m.prefixSeq) > 0 {
		if msg.String() == "esc" {
			m.prefixSeq = nil
			return m, nil
		}
		if len(msg.Runes) > 0 {
			newSeq := append(append([]rune(nil), m.prefixSeq...), msg.Runes[0])
			if cmd, ok := findCommand(newSeq); ok {
				if cmd.execute != nil {
					m.prefixSeq = nil
					return cmd.execute(m)
				}
				if len(cmd.children) > 0 {
					m.prefixSeq = newSeq
					return m, nil
				}
			}
		}
		m.prefixSeq = nil
		return m, nil
	}

	switch msg.String() {
	case "ctrl+c":
		if m.buf.Dirty() && !m.checkingQuit {
			m.checkingQuit = true
			return m, m.fetchClientCount()
		}
		if !m.checkingQuit {
			m.quitting = true
			return m, tea.Sequence(m.doDisconnect(), tea.Quit)
		}

	case ":":
		m.mode = ModeCommand
		m.cmdBuf = ""

	case "esc":
		m.sel = nil

	// Enter insert mode — clear any selection and start an undo group.
	case "i":
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.mode = ModeInsert

	case "A":
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.mode = ModeInsert
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	case "o":
		m.sel = nil
		m.currentGroup = []document.Op{}
		m.mode = ModeInsert
		line := m.cursor.Line
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: line,
			InsertCol:  m.buf.LineLen(line),
			InsertText: "\n",
		}
		m.cursor = document.Pos{Line: line + 1, Col: 0}
		return applyOp(m, op)

	case "O":
		m.sel = nil
		m.currentGroup = []document.Op{}
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
		m.cursor = document.Pos{Line: m.cursor.Line, Col: 0}
		return applyOp(m, op)

	case "ctrl+s":
		return m, m.doSave()

	case "u":
		if len(m.undoStack) > 0 {
			group := m.undoStack[len(m.undoStack)-1]
			m.undoStack = m.undoStack[:len(m.undoStack)-1]
			m.sel = nil
			var cmds []tea.Cmd
			var redoGroup []document.Op
			for i := len(group) - 1; i >= 0; i-- {
				inv := group[i]
				inv.ClientID = m.rpc.ClientID()
				reInv := inverseOp(m, inv) // compute re-inverse before applying
				m.buf.Apply(inv)
				cmds = append(cmds, m.sendToServer(inv))
				redoGroup = append(redoGroup, reInv)
			}
			m.redoStack = append(m.redoStack, redoGroup)
			if len(group) > 0 {
				first := group[0]
				if first.Type == document.OpDelete {
					m.cursor = document.Pos{Line: first.FromLine, Col: first.FromCol}
				} else {
					m.cursor = document.Pos{Line: first.InsertLine, Col: first.InsertCol}
				}
				m.scrollToCursor()
			}
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			return m, tea.Sequence(cmds...)
		}

	case "U":
		if len(m.redoStack) > 0 {
			group := m.redoStack[len(m.redoStack)-1]
			m.redoStack = m.redoStack[:len(m.redoStack)-1]
			m.sel = nil
			var cmds []tea.Cmd
			var newUndoGroup []document.Op
			for i := len(group) - 1; i >= 0; i-- {
				op := group[i]
				op.ClientID = m.rpc.ClientID()
				inv := inverseOp(m, op) // compute inverse before applying
				m.buf.Apply(op)
				cmds = append(cmds, m.sendToServer(op))
				newUndoGroup = append(newUndoGroup, inv)
			}
			m.undoStack = append(m.undoStack, newUndoGroup)
			if len(group) > 0 {
				first := group[len(group)-1] // first applied during redo
				if first.Type == document.OpInsert {
					m.cursor = document.Pos{Line: first.InsertLine, Col: first.InsertCol}
				} else {
					m.cursor = document.Pos{Line: first.FromLine, Col: first.FromCol}
				}
				m.scrollToCursor()
			}
			if len(m.undoStack) == m.savedUndoDepth {
				m.buf.SetClean()
			}
			return m, tea.Sequence(cmds...)
		}

	// Selection: w = next word, x = current line.
	case "w":
		m.selectWord()

	case "x":
		m.selectLine()

	// Operators: act on selection (no-op if nothing selected).
	case "d":
		m2, cmd := m.deleteSelection()
		return m2, cmd

	case "c":
		m.currentGroup = []document.Op{}
		m2, cmd := m.deleteSelection()
		m2.mode = ModeInsert
		return m2, cmd

	// Movement — clears selection.
	case "h", "left":
		m.sel = nil
		m.moveCursor(0, -1)
	case "l", "right":
		m.sel = nil
		m.moveCursor(0, 1)
	case "j", "down":
		m.sel = nil
		m.moveCursor(1, 0)
	case "k", "up":
		m.sel = nil
		m.moveCursor(-1, 0)
	case "ctrl+f", "pgdown":
		m.sel = nil
		m.moveCursor(m.visibleLines(), 0)
	case "ctrl+b", "pgup":
		m.sel = nil
		m.moveCursor(-m.visibleLines(), 0)
	case "g":
		m.sel = nil
		m.cursor = document.Pos{Line: 0, Col: 0}
		m.topLine = 0
	case "G":
		m.sel = nil
		last := max(0, m.buf.LineCount()-1)
		m.cursor = document.Pos{Line: last, Col: 0}
		m.scrollToCursor()
	case "0", "^", "home":
		m.sel = nil
		m.cursor.Col = 0
	case "$", "end":
		m.sel = nil
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	default:
		// Check whether this key starts a prefix command sequence.
		if len(msg.Runes) > 0 {
			r := msg.Runes[0]
			for _, c := range prefixCmds {
				if c.key == r {
					m.prefixSeq = []rune{r}
					return m, nil
				}
			}
		}
	}
	return m, nil
}

func (m Model) handleInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		if m.cursor.Col > 0 {
			m.cursor.Col--
		}
		// Commit the undo group accumulated during this Insert session.
		if len(m.currentGroup) > 0 {
			m.undoStack = append(m.undoStack, m.currentGroup)
		}
		m.currentGroup = nil
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
			return applyOp(m, op)
		} else if m.cursor.Line > 0 {
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
			return applyOp(m, op)
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
			return applyOp(m, op)
		} else if m.cursor.Line < m.buf.LineCount()-1 {
			op := document.Op{
				ClientID: m.rpc.ClientID(),
				Type:     document.OpDelete,
				FromLine: m.cursor.Line,
				FromCol:  lineLen,
				ToLine:   m.cursor.Line + 1,
				ToCol:    0,
			}
			return applyOp(m, op)
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
		return applyOp(m, op)

	case "tab":
		op := document.Op{
			ClientID:   m.rpc.ClientID(),
			Type:       document.OpInsert,
			InsertLine: m.cursor.Line,
			InsertCol:  m.cursor.Col,
			InsertText: "\t",
		}
		m.cursor.Col++
		return applyOp(m, op)

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
			return applyOp(m, op)
		}
	}
	return m, nil
}

func (m Model) handleCommand(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = ModeNormal
		m.cmdBuf = ""
	case "enter":
		return m.executeCommand()
	case "backspace":
		runes := []rune(m.cmdBuf)
		if len(runes) > 0 {
			m.cmdBuf = string(runes[:len(runes)-1])
		} else {
			m.mode = ModeNormal
		}
	default:
		if len(msg.Runes) > 0 {
			m.cmdBuf += string(msg.Runes)
		}
	}
	return m, nil
}

func (m Model) executeCommand() (tea.Model, tea.Cmd) {
	cmd := strings.TrimSpace(m.cmdBuf)
	m.mode = ModeNormal
	m.cmdBuf = ""

	// Bare number → go to line.
	if n, err := strconv.Atoi(cmd); err == nil {
		lc := m.displayLineCount()
		if n < 1 || n > lc {
			m.status = fmt.Sprintf("E: line %d out of range (1-%d)", n, lc)
			return m, nil
		}
		m.cursor = document.Pos{Line: n - 1, Col: 0}
		m.scrollToCursor()
		return m, nil
	}

	switch cmd {
	case "w", "write":
		return m, m.doSave()
	case "q", "quit":
		if m.buf.Dirty() {
			m.status = "E: unsaved changes (use :q! to discard)"
			return m, nil
		}
		m.quitting = true
		return m, tea.Sequence(m.doDisconnect(), tea.Quit)
	case "q!":
		m.quitting = true
		return m, tea.Sequence(m.doDisconnect(), tea.Quit)
	case "wq", "wqa", "x":
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			m.rpc.Save(ctx, m.bufID)
			return saveAndQuitMsg{}
		}
	case "metrics":
		if m.metrics != nil {
			m.metrics.show = !m.metrics.show
		}
	default:
		m.status = fmt.Sprintf("E: unknown command: %s", cmd)
	}
	return m, nil
}

// sendOp applies the op locally and sends it to the server.
func (m Model) sendOp(op document.Op) tea.Cmd {
	m.buf.Apply(op)
	return m.sendToServer(op)
}

// sendToServer sends op to the server without applying it locally.
// Used by undo when local apply is handled separately.
func (m Model) sendToServer(op document.Op) tea.Cmd {
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

// applyOp records the inverse of op for undo, then applies op locally and
// queues an async send to the server.
//
// If m.currentGroup is non-nil (i.e. we are inside an Insert session), the
// inverse is appended to the group instead of pushed immediately; the group is
// committed to undoStack when the user presses ESC.
func applyOp(m Model, op document.Op) (Model, tea.Cmd) {
	inv := inverseOp(m, op)
	if m.currentGroup != nil {
		m.currentGroup = append(m.currentGroup, inv)
	} else {
		m.undoStack = append(m.undoStack, []document.Op{inv})
	}
	m.redoStack = nil // any new edit invalidates the redo history
	return m, tea.Batch(m.sendOp(op), m.reparseHighlight())
}

// inverseOp returns the op that reverses op.
// For OpInsert the inverse is an OpDelete of the same span.
// For OpDelete the inverse is an OpInsert of the text that was there.
// The buffer must NOT yet have op applied when this is called.
func inverseOp(m Model, op document.Op) document.Op {
	switch op.Type {
	case document.OpInsert:
		toLine, toCol := insertEndPos(op.InsertLine, op.InsertCol, op.InsertText)
		return document.Op{
			Type:     document.OpDelete,
			FromLine: op.InsertLine,
			FromCol:  op.InsertCol,
			ToLine:   toLine,
			ToCol:    toCol,
		}
	case document.OpDelete:
		return document.Op{
			Type:       document.OpInsert,
			InsertLine: op.FromLine,
			InsertCol:  op.FromCol,
			InsertText: bufText(m, op.FromLine, op.FromCol, op.ToLine, op.ToCol),
		}
	}
	return document.Op{Type: document.OpNoop}
}

// insertEndPos returns the buffer position immediately after inserting text
// starting at (fromLine, fromCol).
func insertEndPos(fromLine, fromCol int, text string) (toLine, toCol int) {
	toLine, toCol = fromLine, fromCol
	for _, r := range text {
		if r == '\n' {
			toLine++
			toCol = 0
		} else {
			toCol++
		}
	}
	return
}

// bufText extracts the text in [fromLine:fromCol, toLine:toCol) from the buffer.
func bufText(m Model, fromLine, fromCol, toLine, toCol int) string {
	if fromLine == toLine {
		runes := []rune(m.buf.Line(fromLine))
		end := min(toCol, len(runes))
		start := min(fromCol, end)
		return string(runes[start:end])
	}
	var sb strings.Builder
	first := []rune(m.buf.Line(fromLine))
	if fromCol <= len(first) {
		sb.WriteString(string(first[fromCol:]))
	}
	sb.WriteByte('\n')
	for l := fromLine + 1; l < toLine; l++ {
		sb.WriteString(m.buf.Line(l))
		sb.WriteByte('\n')
	}
	last := []rune(m.buf.Line(toLine))
	sb.WriteString(string(last[:min(toCol, len(last))]))
	return sb.String()
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

func (m Model) reparseHighlight() tea.Cmd {
	if m.hlr == nil {
		return nil
	}
	content := []byte(m.buf.Content())
	hlr := m.hlr
	return func() tea.Msg {
		start := time.Now()
		spans := hlr.Highlight(content)
		return highlightMsg{spans: spans, duration: time.Since(start)}
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

// clickToPos converts a terminal (x, y) coordinate to a buffer position.
// Returns ok=false if the click is outside the text area (e.g. on the status bar).
func (m *Model) clickToPos(x, y int) (document.Pos, bool) {
	if y >= m.height-1 {
		return document.Pos{}, false
	}
	lineNum := m.topLine + y
	disp := m.displayLineCount()
	if lineNum >= disp {
		lineNum = max(0, disp-1)
	}
	col := max(0, x-m.gutterWidth())
	lineLen := m.buf.LineLen(lineNum)
	maxCol := lineLen
	if m.mode == ModeNormal && maxCol > 0 {
		maxCol--
	}
	col = min(col, maxCol)
	return document.Pos{Line: lineNum, Col: col}, true
}

func (m *Model) handleMousePress(x, y int) {
	pos, ok := m.clickToPos(x, y)
	if !ok {
		return
	}
	m.cursor = pos
	m.sel = &Selection{Anchor: pos, Head: pos}
	m.dragging = true
}

func (m *Model) handleMouseDrag(x, y int) {
	pos, ok := m.clickToPos(x, y)
	if !ok {
		return
	}
	m.cursor = pos
	if m.sel != nil {
		m.sel.Head = pos
	}
}

// ---- selection helpers ----

// selectWord selects the word at the cursor. If a word is already selected,
// advances to the next word on the same line.
func (m *Model) selectWord() {
	runes := []rune(m.buf.Line(m.cursor.Line))
	lineNum := m.cursor.Line
	if m.sel != nil && !m.sel.IsLine {
		// Advance: find next word after the current selection head.
		_, end := m.sel.ordered()
		if end.Line == lineNum {
			s, e, ok := findNextWordFrom(runes, end.Col)
			if ok {
				m.sel = &Selection{Anchor: document.Pos{Line: lineNum, Col: s}, Head: document.Pos{Line: lineNum, Col: e}}
				m.cursor = m.sel.Head
				m.scrollToCursor()
				return
			}
		}
	}
	// Fresh word select from cursor.
	s, e, ok := findWordAt(runes, m.cursor.Col)
	if ok {
		m.sel = &Selection{Anchor: document.Pos{Line: lineNum, Col: s}, Head: document.Pos{Line: lineNum, Col: e}}
		m.cursor = m.sel.Head
		m.scrollToCursor()
	}
}

// selectLine selects the entire current line.
func (m *Model) selectLine() {
	line := m.cursor.Line
	lineLen := m.buf.LineLen(line)
	m.sel = &Selection{
		Anchor: document.Pos{Line: line, Col: 0},
		Head:   document.Pos{Line: line, Col: max(0, lineLen-1)},
		IsLine: true,
	}
	m.cursor = m.sel.Head
}

// deleteSelection deletes the selected text and returns the updated model + cmd.
// If there is no selection the model is returned unchanged.
func (m Model) deleteSelection() (Model, tea.Cmd) {
	if m.sel == nil {
		lineLen := m.buf.LineLen(m.cursor.Line)
		if lineLen == 0 {
			return m, nil
		}
		col := min(m.cursor.Col, lineLen-1)
		m.sel = &Selection{
			Anchor: document.Pos{Line: m.cursor.Line, Col: col},
			Head:   document.Pos{Line: m.cursor.Line, Col: col},
		}
	}
	start, end := m.sel.ordered()
	op := document.Op{
		ClientID: m.rpc.ClientID(),
		Type:     document.OpDelete,
		FromLine: start.Line,
		FromCol:  start.Col,
	}
	if m.sel.IsLine {
		lc := m.buf.LineCount()
		if end.Line+1 < lc {
			op.ToLine = end.Line + 1
			op.ToCol = 0
		} else {
			op.ToLine = end.Line
			op.ToCol = m.buf.LineLen(end.Line)
		}
	} else {
		lineLen := m.buf.LineLen(end.Line)
		if end.Col+1 <= lineLen {
			op.ToLine = end.Line
			op.ToCol = end.Col + 1
		} else {
			lc := m.buf.LineCount()
			if end.Line+1 < lc {
				op.ToLine = end.Line + 1
				op.ToCol = 0
			} else {
				op.ToLine = end.Line
				op.ToCol = lineLen
			}
		}
	}
	m.cursor = start
	m.sel = nil
	return applyOp(m, op)
}

// ---- word finding ----

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// findWordAt returns the inclusive [start, end] of the word at or ahead of col.
func findWordAt(runes []rune, col int) (start, end int, found bool) {
	n := len(runes)
	if n == 0 {
		return -1, -1, false
	}
	i := min(col, n-1)
	if !isWordChar(runes[i]) {
		// Not on a word char: skip forward to the next word.
		for i < n && !isWordChar(runes[i]) {
			i++
		}
		if i >= n {
			return -1, -1, false
		}
	}
	// Select from here (cursor or start of next word) to end of word.
	start = i
	for i < n && isWordChar(runes[i]) {
		i++
	}
	return start, i - 1, true
}

// findNextWordFrom returns the inclusive [start, end] of the next word that
// begins strictly after afterEnd.
func findNextWordFrom(runes []rune, afterEnd int) (start, end int, found bool) {
	n := len(runes)
	i := afterEnd + 1
	// Skip any remaining word chars (tail of current word).
	for i < n && isWordChar(runes[i]) {
		i++
	}
	// Skip non-word chars.
	for i < n && !isWordChar(runes[i]) {
		i++
	}
	if i >= n {
		return -1, -1, false
	}
	start = i
	for i < n && isWordChar(runes[i]) {
		i++
	}
	return start, i - 1, true
}

// ---- selection rendering helpers ----

// selectionCols returns the inclusive [selA, selB] column range selected on
// lineNum, or (-1, -1) when lineNum is not inside the current selection.
func (m Model) selectionCols(lineNum, lineLen int) (selA, selB int) {
	if m.sel == nil {
		return -1, -1
	}
	start, end := m.sel.ordered()
	if lineNum < start.Line || lineNum > end.Line {
		return -1, -1
	}
	selA = 0
	if lineNum == start.Line {
		selA = start.Col
	}
	selB = max(0, lineLen-1)
	if !m.sel.IsLine && lineNum == end.Line {
		selB = min(end.Col, max(0, lineLen-1))
	}
	if lineLen == 0 || selA > selB {
		return -1, -1
	}
	return selA, selB
}

// expandTabsRemap replaces tab runes with spaces (tabWidth-column tab stops).
// Returns the expanded slice and colMap where colMap[i] = visual column of original rune i.
// colMap has len(runes)+1 entries; colMap[len(runes)] is the total visual width.
func expandTabsRemap(runes []rune) (expanded []rune, colMap []int) {
	colMap = make([]int, len(runes)+1)
	vcol := 0
	for i, r := range runes {
		colMap[i] = vcol
		if r == '\t' {
			spaces := tabWidth - (vcol % tabWidth)
			for k := 0; k < spaces; k++ {
				expanded = append(expanded, ' ')
			}
			vcol += spaces
		} else {
			expanded = append(expanded, r)
			vcol++
		}
	}
	colMap[len(runes)] = vcol
	return
}

// renderLineRunes writes runes with selection and cursor highlighting applied.
// selA/selB are inclusive selected column bounds (-1,-1 for no selection).
// curCol is the cursor column (-1 if cursor is not on this line).
func renderLineRunes(sb *strings.Builder, runes []rune, selA, selB, curCol int, spans []highlight.Span) {
	n := len(runes)
	hasCursor := curCol >= 0
	hasSel := selA >= 0

	// spanIdxAt returns the index of the first (highest-priority) span covering col, or -1.
	spanIdxAt := func(col int) int {
		for i, s := range spans {
			if col >= s.StartCol && col < s.EndCol {
				return i
			}
		}
		return -1
	}

	i := 0
	for i < n {
		isCursor := hasCursor && i == curCol
		inSel := hasSel && i >= selA && i <= selB
		switch {
		case isCursor:
			sb.WriteString(cursorStyle.Render(string(runes[i : i+1])))
			i++
		case inSel:
			j := i + 1
			for j < n && !(hasCursor && j == curCol) && j >= selA && j <= selB {
				j++
			}
			sb.WriteString(selectionStyle.Render(string(runes[i:j])))
			i = j
		default:
			// Batch consecutive plain chars with the same highlight span.
			si := spanIdxAt(i)
			j := i + 1
			for j < n {
				if hasCursor && j == curCol {
					break
				}
				if hasSel && j >= selA && j <= selB {
					break
				}
				if spanIdxAt(j) != si {
					break
				}
				j++
			}
			text := string(runes[i:j])
			if si >= 0 {
				sb.WriteString(spans[si].ANSI)
				sb.WriteString(text)
				sb.WriteString(highlight.ANSIReset)
			} else {
				sb.WriteString(text)
			}
			i = j
		}
	}
	if hasCursor && curCol >= n {
		sb.WriteString(cursorStyle.Render(" "))
	}
}

// ---- cursor movement ----

func (m *Model) moveCursor(dLine, dCol int) {
	line := max(0, m.cursor.Line+dLine)
	if line >= m.buf.LineCount() {
		line = max(0, m.buf.LineCount()-1)
	}
	lineLen := m.buf.LineLen(line)
	maxCol := lineLen
	if m.mode == ModeNormal && maxCol > 0 {
		maxCol--
	}
	col := max(0, min(m.cursor.Col+dCol, maxCol))
	m.cursor = document.Pos{Line: line, Col: col}
	m.scrollToCursor()
}

func (m *Model) clampCursor() {
	line := min(m.cursor.Line, max(0, m.buf.LineCount()-1))
	col := min(m.cursor.Col, m.buf.LineLen(line))
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
	m.topLine = max(0, m.topLine)
}

func (m Model) visibleLines() int {
	return max(1, m.height-1)
}

// displayLineCount returns the number of lines that receive a line number.
// A trailing empty line (from a final newline) never gets a number — it only
// becomes numbered once the user types at least one character on it.
func (m Model) displayLineCount() int {
	lc := m.buf.LineCount()
	if lc > 1 && m.buf.Line(lc-1) == "" {
		return lc - 1
	}
	return lc
}

// gutterWidth returns the number of columns reserved for line numbers (0 when disabled).
func (m Model) gutterWidth() int {
	if m.cfg == nil || !m.cfg.LineNumbers {
		return 0
	}
	return len(fmt.Sprint(m.displayLineCount())) + 1
}

// ---- View helpers ----

// renderLine renders screen row i (0-based, relative to topLine) without a trailing newline.
// Tabs are expanded to spaces so that cellbuf correctly measures visual widths.
// Each line is padded to m.width so prior content is always fully overwritten.
func (m Model) renderLine(i int) string {
	lineNum := m.topLine + i
	bufLineCount := m.buf.LineCount()
	dispLineCount := m.displayLineCount()
	gutterW := m.gutterWidth()
	var sb strings.Builder

	padToWidth := func(s string) string {
		if m.width <= 0 {
			return s
		}
		w := lipgloss.Width(s)
		if w < m.width {
			return s + strings.Repeat(" ", m.width-w)
		}
		return s
	}

	if lineNum >= bufLineCount {
		if gutterW > 0 {
			sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
		}
		sb.WriteString("~")
		return padToWidth(sb.String())
	}

	if lineNum >= dispLineCount {
		if gutterW > 0 {
			sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
		}
		if lineNum == m.cursor.Line {
			sb.WriteString(cursorStyle.Render(" "))
		} else {
			sb.WriteString("~")
		}
		return padToWidth(sb.String())
	}

	if gutterW > 0 {
		numStr := fmt.Sprintf("%*d ", gutterW-1, lineNum+1)
		if lineNum == m.cursor.Line {
			sb.WriteString(gutterCurStyle.Render(numStr))
		} else {
			sb.WriteString(gutterStyle.Render(numStr))
		}
	}

	line := m.buf.Line(lineNum)
	runes := []rune(line)
	expandedRunes, colMap := expandTabsRemap(runes)

	// Remap selection columns from rune indices to visual columns.
	selA, selB := m.selectionCols(lineNum, len(runes))
	if selA >= 0 {
		newSelA := colMap[selA]
		var newSelB int
		if selB+1 <= len(runes) {
			newSelB = colMap[selB+1] - 1
		} else {
			newSelB = colMap[len(runes)] - 1
		}
		selA = newSelA
		selB = max(newSelA, newSelB)
	}

	// Remap cursor column from rune index to visual column.
	curCol := -1
	if lineNum == m.cursor.Line {
		c := min(m.cursor.Col, len(runes))
		curCol = colMap[c]
	}

	// Remap highlight spans from rune indices to visual columns.
	var remappedSpans []highlight.Span
	if spans := m.hlSpans[lineNum]; len(spans) > 0 {
		remappedSpans = make([]highlight.Span, len(spans))
		for idx, s := range spans {
			newStart := 0
			if s.StartCol < len(colMap) {
				newStart = colMap[s.StartCol]
			}
			newEnd := math.MaxInt
			if s.EndCol != math.MaxInt {
				if s.EndCol < len(colMap) {
					newEnd = colMap[s.EndCol]
				} else {
					newEnd = colMap[len(runes)]
				}
			}
			remappedSpans[idx] = highlight.Span{StartCol: newStart, EndCol: newEnd, ANSI: s.ANSI}
		}
	}

	renderLineRunes(&sb, expandedRunes, selA, selB, curCol, remappedSpans)
	return padToWidth(sb.String())
}

// renderPopupBox builds the styled lines of a popup menu with rounded borders.
func renderPopupBox(title string, items []command, maxW int) []string {
	// Compute needed inner width.
	minInner := len([]rune(title))
	for _, item := range items {
		w := len([]rune(fmt.Sprintf("  %c  %s  ", item.key, item.label)))
		if w > minInner {
			minInner = w
		}
	}
	innerW := minInner
	if innerW+2 > maxW {
		innerW = max(2, maxW-2)
	}

	// Top border with centered title.
	titleStr := title
	titleRunes := []rune(titleStr)
	if len(titleRunes) > innerW {
		titleRunes = titleRunes[:innerW]
		titleStr = string(titleRunes)
	}
	remaining := max(0, innerW-len(titleRunes))
	top := popupBorderStyle.Render("╭") +
		popupTextStyle.Render(titleStr) +
		popupBorderStyle.Render(strings.Repeat("─", remaining)+"╮")

	lines := []string{top}
	for _, item := range items {
		keyPart := fmt.Sprintf("  %c", item.key)
		sep := "  "
		label := item.label
		// Truncate label if needed.
		maxLabel := innerW - len([]rune(keyPart+sep+"  "))
		if maxLabel < 0 {
			maxLabel = 0
		}
		labelRunes := []rune(label)
		if len(labelRunes) > maxLabel {
			label = string(labelRunes[:maxLabel])
		}
		content := keyPart + sep + label
		padW := innerW - len([]rune(content))
		if padW < 0 {
			padW = 0
		}
		row := popupBorderStyle.Render("│") +
			popupKeyStyle.Render(keyPart) +
			popupTextStyle.Render(sep+label+strings.Repeat(" ", padW)) +
			popupBorderStyle.Render("│")
		lines = append(lines, row)
	}

	bottom := popupBorderStyle.Render("╰" + strings.Repeat("─", innerW) + "╯")
	lines = append(lines, bottom)
	return lines
}

// metricsInnerW is the fixed visible width between the box borders.
// Layout per row: 1 space + 9 label + 1 space + 8 value (right-aligned) + 1 space = 20.
const metricsInnerW = 20

// renderMetricsBox builds the styled lines of the metrics overlay panel.
func renderMetricsBox(m *metricsData) []string {
	fmtDur := func(d time.Duration) string {
		µs := d.Microseconds()
		if µs < 1000 {
			return fmt.Sprintf("%dµs", µs)
		}
		ms := float64(µs) / 1000
		if ms < 1000 {
			return fmt.Sprintf("%.2fms", ms)
		}
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	rows := []struct{ label, val string }{
		{"render   ", fmtDur(m.renderDuration)},
		{"parse    ", fmtDur(m.highlightDuration)},
		{"key→frame", fmtDur(m.keyToFrameDuration)},
	}
	title := "Metrics"
	top := popupBorderStyle.Render("╭") +
		popupTextStyle.Render(title) +
		popupBorderStyle.Render(strings.Repeat("─", metricsInnerW-len([]rune(title)))+"╮")
	lines := []string{top}
	for _, r := range rows {
		// Right-align value in an 8-char field so the panel width never changes.
		valField := fmt.Sprintf("%8s", r.val)
		line := popupBorderStyle.Render("│") +
			popupTextStyle.Render(" "+r.label+" ") +
			popupKeyStyle.Render(valField) +
			popupTextStyle.Render(" ") +
			popupBorderStyle.Render("│")
		lines = append(lines, line)
	}
	lines = append(lines, popupBorderStyle.Render("╰"+strings.Repeat("─", metricsInnerW)+"╯"))
	return lines
}

// overlayRight truncates mainLine to popCol visual columns and appends popupLine.
func overlayRight(mainLine, popupLine string, popCol int) string {
	truncated := ansi.Truncate(mainLine, popCol, "")
	tw := lipgloss.Width(truncated)
	if tw < popCol {
		truncated += strings.Repeat(" ", popCol-tw)
	}
	return truncated + popupLine
}

// ---- View ----

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "loading…"
	}

	// Record timing via the shared pointer so the value receiver can write back.
	if m.metrics != nil {
		viewStart := time.Now()
		defer func() {
			m.metrics.renderDuration = time.Since(viewStart)
			// Only capture key→frame for the first View() after a key press,
			// then zero lastKeyAt so tick-driven redraws don't keep updating it.
			if !m.metrics.lastKeyAt.IsZero() {
				m.metrics.keyToFrameDuration = time.Since(m.metrics.lastKeyAt)
				m.metrics.lastKeyAt = time.Time{}
			}
		}()
	}

	vis := m.visibleLines()
	lines := make([]string, vis)
	for i := range vis {
		lines[i] = m.renderLine(i)
	}

	// Overlay prefix-command popup in the bottom-right corner.
	if len(m.prefixSeq) > 0 {
		if cmd, ok := findCommand(m.prefixSeq); ok && len(cmd.children) > 0 {
			popup := renderPopupBox(cmd.menuTitle, cmd.children, m.width)
			popH := len(popup)
			popW := lipgloss.Width(popup[0])
			popCol := m.width - popW
			if popCol >= 0 {
				startRow := max(0, vis-popH)
				for pi, popLine := range popup {
					row := startRow + pi
					if row < vis {
						lines[row] = overlayRight(lines[row], popLine, popCol)
					}
				}
			}
		}
	}

	// Overlay metrics panel in the top-right corner.
	if m.metrics != nil && m.metrics.show {
		box := renderMetricsBox(m.metrics)
		boxW := lipgloss.Width(box[0])
		boxCol := m.width - boxW
		if boxCol >= 0 {
			for ri, boxLine := range box {
				if ri < vis {
					lines[ri] = overlayRight(lines[ri], boxLine, boxCol)
				}
			}
		}
	}

	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(m.renderStatusBar())
	return sb.String()
}

func (m Model) renderStatusBar() string {
	if m.width == 0 {
		return ""
	}

	if m.mode == ModeCommand {
		prompt := ":" + m.cmdBuf
		promptRunes := []rune(prompt)
		maxPromptW := m.width - 1
		if len(promptRunes) > maxPromptW {
			promptRunes = promptRunes[len(promptRunes)-maxPromptW:]
		}
		padW := max(0, m.width-len(promptRunes)-1)
		return barStyle.Render(string(promptRunes)) + cursorStyle.Render(" ") + barStyle.Width(padW).Render("")
	}

	modeLabel := "NORMAL"
	ms := normalModeStyle
	if m.mode == ModeInsert {
		modeLabel = "INSERT"
		ms = insertModeStyle
	}
	left := ms.Render("  " + modeLabel + "  ")
	leftW := lipgloss.Width(left)

	posStr := fmt.Sprintf("  %d:%d  ", m.cursor.Line+1, m.cursor.Col+1)
	right := barStyle.Render(posStr)
	rightW := lipgloss.Width(right)

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

	centerW := max(0, m.width-leftW-rightW)
	center := barStyle.Width(centerW).Align(lipgloss.Center).Render(centerContent)

	return left + center + right
}
