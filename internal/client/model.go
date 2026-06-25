package client

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
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

// Selection tracks the selected range in the buffer.
// [Anchor, Head] is inclusive on both ends for display purposes.
// IsLine marks a linewise selection (x) which deletes the entire line + newline.
type Selection struct {
	Anchor document.Pos
	Head   document.Pos
	IsLine bool
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
