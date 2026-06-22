package client

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/twist/internal/config"
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

	selectionStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#2D5F8A")).
			Foreground(lipgloss.Color("#FFFFFF"))

	gutterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#606060"))

	gutterCurStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#AAAAAA"))
)

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

// Model is the Bubble Tea model for a single buffer view.
type Model struct {
	rpc          *RPC
	buf          *document.Buffer
	cfg          *config.Config
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
	sel          *Selection
	dragging     bool
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

	case "esc":
		m.sel = nil

	// Enter insert mode — clear any selection first.
	case "i":
		m.sel = nil
		m.mode = ModeInsert

	case "a":
		m.sel = nil
		m.mode = ModeInsert
		m.cursor.Col = m.buf.LineLen(m.cursor.Line)

	case "o":
		m.sel = nil
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
		m.sel = nil
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
	case "0", "home":
		m.sel = nil
		m.cursor.Col = 0
	case "$", "end":
		m.sel = nil
		m.cursor.Col = max(0, m.buf.LineLen(m.cursor.Line)-1)
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
		return m, nil
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
	return m, m.sendOp(op)
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
		// Skip non-word chars forward.
		for i < n && !isWordChar(runes[i]) {
			i++
		}
		if i >= n {
			return -1, -1, false
		}
	} else {
		// Walk back to word start.
		for i > 0 && isWordChar(runes[i-1]) {
			i--
		}
	}
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

// renderLineRunes writes runes with selection and cursor highlighting applied.
// selA/selB are inclusive selected column bounds (-1,-1 for no selection).
// curCol is the cursor column (-1 if cursor is not on this line).
func renderLineRunes(sb *strings.Builder, runes []rune, selA, selB, curCol int) {
	n := len(runes)
	hasCursor := curCol >= 0
	hasSel := selA >= 0
	i := 0
	for i < n {
		isCursor := hasCursor && i == curCol
		inSel := hasSel && i >= selA && i <= selB
		switch {
		case isCursor:
			sb.WriteString(cursorStyle.Render(string(runes[i : i+1])))
			i++
		case inSel:
			// Batch consecutive selected chars (stopping before cursor).
			j := i + 1
			for j < n && !(hasCursor && j == curCol) && j >= selA && j <= selB {
				j++
			}
			sb.WriteString(selectionStyle.Render(string(runes[i:j])))
			i = j
		default:
			// Batch consecutive plain chars.
			j := i + 1
			for j < n && !(hasCursor && j == curCol) && !(hasSel && j >= selA && j <= selB) {
				j++
			}
			sb.WriteString(string(runes[i:j]))
			i = j
		}
	}
	// Cursor past end of line.
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

// ---- View ----

func (m Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "loading…"
	}

	vis := m.visibleLines()
	bufLineCount := m.buf.LineCount()
	dispLineCount := m.displayLineCount()
	gutterW := m.gutterWidth()
	var sb strings.Builder

	for i := range vis {
		lineNum := m.topLine + i

		// Past all buffer content: show tilde.
		if lineNum >= bufLineCount {
			if gutterW > 0 {
				sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
			}
			sb.WriteString("~\n")
			continue
		}

		// Trailing empty line: no number. Show cursor if it's here, tilde otherwise.
		if lineNum >= dispLineCount {
			if gutterW > 0 {
				sb.WriteString(gutterStyle.Render(strings.Repeat(" ", gutterW)))
			}
			if lineNum == m.cursor.Line {
				sb.WriteString(cursorStyle.Render(" "))
			} else {
				sb.WriteString("~")
			}
			sb.WriteByte('\n')
			continue
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
		selA, selB := m.selectionCols(lineNum, len(runes))
		curCol := -1
		if lineNum == m.cursor.Line {
			curCol = min(m.cursor.Col, len(runes))
		}
		renderLineRunes(&sb, runes, selA, selB, curCol)
		sb.WriteByte('\n')
	}

	sb.WriteString(m.renderStatusBar())

	return sb.String()
}

func (m Model) renderStatusBar() string {
	if m.width == 0 {
		return ""
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
