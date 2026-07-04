package client

import (
	"context"
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
	ModeSearch
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

// savedAsMsg signals a successful save-as; carries the new path.
type savedAsMsg struct {
	newPath    string
	thenClose  bool
}

// clientCountMsg carries the result of a bufferClientCount RPC.
type clientCountMsg struct{ count uint32 }

// discardRecoveryMsg carries original file content after the server discards the recovery file.
type discardRecoveryMsg struct{ content string }

// diagnosticsMsg carries fresh diagnostics from the server.
type diagnosticsMsg struct {
	diags    []ClientDiag
	lspReady bool // true only when the LSP client process is actually running
}

// hoverMsg carries a hover result.
type hoverMsg struct{ result ClientHoverResult }

// sigHelpMsg carries a signature-help result (nil Signatures = dismiss).
type sigHelpMsg struct{ help *ClientSigHelp }

// completionsMsg carries fresh completion items.
type completionsMsg struct{ items []ClientCompletion }

// triggerCompletionMsg fires after the auto-trigger debounce delay.
type triggerCompletionMsg struct{}

// definitionMsg carries the result of a go-to-definition request.
type definitionMsg struct {
	loc   ClientLocation
	found bool
}

// CloseBufferMsg signals the App that this buffer wants to close.
// The App decides whether to remove it from the list or quit entirely.
type formatResultMsg struct {
	content     string
	changed     bool
	thenSave    bool
	noFormatter bool
}

type CloseBufferMsg struct{}

// OpenFileAtMsg signals the App to open a file at a specific 0-based line,
// reusing an existing buffer if the file is already open.
type OpenFileAtMsg struct {
	Path string
	Line int
}

// OpenPickerMsg signals the App to open the file picker.
type OpenPickerMsg struct{}

// GrepMsg signals the App to open the workspace search picker.
// Pattern uses the same syntax as within-buffer search: plain text for
// literal (smart-case), or \expr\ for Go regexp.
// Glob optionally restricts which files are searched (e.g. "*.go", "src/").
type GrepMsg struct {
	Pattern string
	Glob    string
}

// NextBufferMsg signals the App to switch to the next buffer.
type NextBufferMsg struct{}

// PrevBufferMsg signals the App to switch to the previous buffer.
type PrevBufferMsg struct{}

// QuitAllMsg signals the App to quit all buffers (:qa / :qa! / :wqa).
type QuitAllMsg struct {
	Force   bool // :qa! — skip dirty check
	SaveAll bool // :wqa — save dirty buffers first
}

// pluginKeyResultMsg carries the result of a plugin key RPC call.
type pluginKeyResultMsg struct{ result PluginKeyResult }

// decorationsMsg carries fresh plugin decorations from the server.
type decorationsMsg struct{ items []ClientDecoration }

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

	diagErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	diagWarnStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFDD44"))
	diagInfoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#88AAFF"))

	// Status bar right-side indicators.
	fileTypeStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#065A96")).
			Foreground(lipgloss.Color("#CCDDFF"))

	lspIdleStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#065A96")).
			Foreground(lipgloss.Color("#667788")) // dim — configured but not yet confirmed running

	lspActiveStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#065A96")).
			Foreground(lipgloss.Color("#AAFFAA")) // green — confirmed running
)

// Selection tracks the selected range in the buffer.
// [Anchor, Head] is inclusive on both ends for display purposes.
// IsLine marks a linewise selection (x) which deletes the entire line + newline.
type Selection struct {
	Anchor document.Pos
	Head   document.Pos
	IsLine bool
}

// ExtraCursor represents a secondary cursor with an optional selection.
// Extra cursors are created by Ctrl+D (next occurrence), C (cursor below),
// and Alt+s (split selection). Esc in Normal mode collapses all extra cursors.
type ExtraCursor struct {
	pos document.Pos
	sel *Selection
}

// cursorSnapshot captures the full cursor state (primary + selection + extras)
// at a point in time, used to restore the editing context on undo/redo.
type cursorSnapshot struct {
	cursor document.Pos
	sel    *Selection
	extras []ExtraCursor
}

// undoEntry pairs a group of inverse ops with the cursor state from before
// the edit, so undo can restore both the buffer content and all cursor positions.
type undoEntry struct {
	ops    []document.Op
	before cursorSnapshot
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
	warnQuit       bool   // showing unsaved-changes warning
	checkingQuit   bool // client-count RPC in flight
	sel            *Selection
	dragging       bool
	lastClickAt    time.Time
	lastClickPos   document.Pos
	undoStack      []undoEntry   // each entry is inverse ops + pre-edit cursor snapshot
	redoStack      []undoEntry   // mirrors undoStack; cleared on any new edit
	currentGroup   []document.Op // non-nil while accumulating ops for the current Insert session
	groupBefore    cursorSnapshot // cursor state when currentGroup was opened
	savedUndoDepth int           // len(undoStack) at the time of the last save
	cmdBuf         string          // text typed after ':' while in ModeCommand
	prefixSeq      []rune          // keys typed so far for a multi-key Normal-mode command
	searchQuery    string
	searchMatches  []searchMatch
	searchIdx      int
	searchOrigin   document.Pos
	searchErr      string // non-empty when regex fails to compile
	hlr            *highlight.Highlighter
	hlSpans        highlight.LineSpans
	metrics        *metricsData
	recoveryPrompt bool // waiting for user to accept or discard recovery content

	// Plugin decorations
	decorations []ClientDecoration
	decorTick   int // poll every 3 ticks (~360ms)

	// Capture mode: plugin owns the next N keypresses.
	captureMode      bool
	captureRemaining uint32

	// Multi-cursor state
	extraCursors []ExtraCursor

	// Save-as dialog: non-nil while the "Save As" popup is visible.
	saveAsInput     *string // current text typed in the dialog
	saveAsThenClose bool    // close the buffer after a successful save-as

	// LSP state
	diagnostics []ClientDiag
	diagTick         int            // counter; fetch every 10 ticks (~1.2s)
	lspActive        bool           // true once first diagnostic poll returns (LSP is running)
	hoverContent     *string        // non-nil = hover popup visible
	hoverScroll      int            // scroll offset within the hover popup
	hoverTotalLines  int            // total rendered body lines; used to clamp scroll
	sigHelp          *ClientSigHelp // non-nil = signature help popup visible
	completions      []ClientCompletion
	completionOn     bool
	completionIdx    int
	completionPrefix string
}

// New creates a Model after the buffer is already open with the server.
func New(rpc *RPC, bufID uint32, content string, version uint64, filePath string, cfg *config.Config, fromRecovery bool) Model {
	buf := document.New(filePath, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	return Model{
		rpc:            rpc,
		buf:            buf,
		cfg:            cfg,
		bufID:          bufID,
		version:        version,
		filePath:       filePath,
		hlr:            highlight.New(filePath),
		metrics:        &metricsData{},
		recoveryPrompt: fromRecovery,
	}
}

// Dirty reports whether the buffer has unsaved changes.
func (m Model) Dirty() bool { return m.buf.Dirty() }

// cursorSnap captures the current cursor, selection, and extra-cursor state.
func (m Model) cursorSnap() cursorSnapshot {
	var selCopy *Selection
	if m.sel != nil {
		s := *m.sel
		selCopy = &s
	}
	return cursorSnapshot{
		cursor: m.cursor,
		sel:    selCopy,
		extras: append([]ExtraCursor(nil), m.extraCursors...),
	}
}

// FilePath returns the absolute path of the file this buffer is editing.
func (m Model) FilePath() string { return m.filePath }

// BufID returns the server-assigned buffer identifier.
func (m Model) BufID() uint32 { return m.bufID }

// AtLine moves the initial cursor to the given 0-based line number.
func (m Model) AtLine(line int) Model {
	line = max(0, min(line, m.buf.LineCount()-1))
	m.cursor = document.Pos{Line: line, Col: 0}
	m.scrollToCursor()
	return m
}

// AtMatch positions the cursor at (line, col), selects matchLen runes, and
// scrolls so the match line sits ~1/4 down from the top of the visible area.
func (m Model) AtMatch(line, col, matchLen, bufHeight int) Model {
	line = max(0, min(line, m.buf.LineCount()-1))
	lineLen := m.buf.LineLen(line)
	col = max(0, min(col, lineLen))
	m.cursor = document.Pos{Line: line, Col: col}
	quarter := max(1, bufHeight/4)
	m.topLine = max(0, line-quarter)
	if matchLen > 0 {
		endCol := min(col+matchLen-1, max(0, lineLen-1))
		if endCol >= col {
			m.sel = &Selection{
				Anchor: document.Pos{Line: line, Col: col},
				Head:   document.Pos{Line: line, Col: endCol},
			}
		}
	}
	return m
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
		return m, m.updateViewportCmd()

	case tickMsg:
		m.diagTick++
		m.decorTick++
		cmds := []tea.Cmd{m.fetchUpdates(), tick()}
		if m.diagTick%10 == 0 {
			cmds = append(cmds, m.fetchDiagnostics())
		}
		if m.decorTick%3 == 0 {
			cmds = append(cmds, m.fetchDecorations())
		}
		return m, tea.Batch(cmds...)

	case updatesMsg:
		for _, op := range msg.ops {
			m.buf.Apply(op)
		}
		m.version = msg.version
		m.clampCursor()
		return m, m.reparseHighlight()

	case saveAsPromptMsg:
		s := ""
		m.saveAsInput = &s
		m.saveAsThenClose = msg.thenClose
		return m, nil

	case errorMsg:
		m.status = "ERR: " + msg.err.Error()
		return m, nil

	case savedMsg:
		m.buf.SetClean()
		m.status = ""
		m.savedUndoDepth = len(m.undoStack)
		return m, nil

	case savedAsMsg:
		m.filePath = msg.newPath
		m.saveAsThenClose = false
		m.buf.SetClean()
		m.status = ""
		m.savedUndoDepth = len(m.undoStack)
		if msg.thenClose {
			return m, m.doCloseBuffer()
		}
		return m, nil

	case clientCountMsg:
		m.checkingQuit = false
		if msg.count <= 1 {
			m.warnQuit = true
		} else {
			// Another client has the buffer — safe to close without warning.
			return m, m.doCloseBuffer()
		}
		return m, nil

	case highlightMsg:
		m.hlSpans = msg.spans
		if m.metrics != nil {
			m.metrics.highlightDuration = msg.duration
		}
		return m, nil

	case discardRecoveryMsg:
		m.buf = document.New(m.filePath, msg.content)
		m.version = 0
		m.undoStack = nil
		m.redoStack = nil
		m.currentGroup = nil
		m.savedUndoDepth = 0
		return m, m.reparseHighlight()

	case diagnosticsMsg:
		m.diagnostics = msg.diags
		if msg.lspReady {
			m.lspActive = true
		}
		return m, nil

	case hoverMsg:
		if msg.result.Found && msg.result.Contents != "" {
			m.hoverContent = &msg.result.Contents
			m.hoverScroll = 0
			m.hoverTotalLines = len(hoverBodyLines(msg.result.Contents, m.width))
		} else if !msg.result.Found {
			m.status = "No hover info"
		}
		return m, nil

	case sigHelpMsg:
		m.sigHelp = msg.help
		return m, nil

	case completionsMsg:
		if len(msg.items) == 0 {
			m.completionOn = false
			m.completions = nil
		} else {
			m.completions = msg.items
			m.completionOn = true
			m.completionIdx = 0
		}
		return m, nil

	case triggerCompletionMsg:
		return m, m.fetchCompletions()

	case formatResultMsg:
		if msg.changed {
			m.buf = document.New(m.filePath, msg.content)
			m.undoStack = nil
			m.redoStack = nil
			m.currentGroup = nil
			m.savedUndoDepth = 0
			m.cursor = document.Pos{
				Line: min(m.cursor.Line, m.buf.LineCount()-1),
			}
			m.scrollToCursor()
			if !msg.thenSave {
				m.status = "Formatted"
			}
		} else if !msg.thenSave {
			if msg.noFormatter {
				m.status = "No formatter available"
			} else {
				m.status = "Already formatted"
			}
		}
		if msg.thenSave {
			return m, m.doSaveNow()
		}
		return m, nil

	case definitionMsg:
		if !msg.found {
			m.status = "No definition found"
			return m, nil
		}
		if msg.loc.Path == m.filePath {
			m.cursor = document.Pos{Line: msg.loc.Line, Col: msg.loc.Col}
			m.scrollToCursor()
			return m, nil
		}
		// Different file: ask the App to open it in a new buffer.
		loc := msg.loc
		return m, func() tea.Msg {
			return OpenFileAtMsg{Path: loc.Path, Line: loc.Line}
		}

	case tea.MouseMsg:
		prevTopLine := m.topLine
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
		if m.topLine != prevTopLine {
			return m, m.updateViewportCmd()
		}
		return m, nil

	case decorationsMsg:
		m.decorations = msg.items
		return m, nil

	case pluginKeyResultMsg:
		// Update capture mode based on what the plugin requested.
		if msg.result.CaptureKeys > 0 {
			m.captureMode = true
			m.captureRemaining = msg.result.CaptureKeys
		} else {
			m.captureMode = false
			m.captureRemaining = 0
		}
		cmds := []tea.Cmd{m.fetchDecorations()}
		if msg.result.HasCursor {
			m.cursor = document.Pos{
				Line: int(msg.result.CursorLine),
				Col:  int(msg.result.CursorCol),
			}
			m.scrollToCursor()
			cmds = append(cmds, m.updateViewportCmd())
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		if m.metrics != nil {
			m.metrics.lastKeyAt = time.Now()
		}
		prevTopLine := m.topLine
		newModel, cmd := m.handleKey(msg)
		if nm, ok := newModel.(Model); ok && nm.topLine != prevTopLine {
			return nm, tea.Batch(cmd, nm.updateViewportCmd())
		}
		return newModel, cmd
	}
	return m, nil
}

func (m Model) fetchDiagnostics() tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := m.rpc.GetDiagnostics(ctx, bufID)
		if err != nil {
			return nil
		}
		return diagnosticsMsg{diags: result.Diags, lspReady: result.LspReady}
	}
}

// fetchDecorations polls all plugin DecorationProviders for the current buffer/viewport.
// The current viewport is sent inline before fetching so the server always uses
// an up-to-date range — sending both from the same goroutine guarantees ordering.
func (m Model) fetchDecorations() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	bufID := m.bufID
	topLine := uint32(m.topLine)
	height := uint32(m.height)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.UpdateViewport(ctx, topLine, height)
		items, err := m.rpc.GetDecorations(ctx, bufID)
		if err != nil {
			return nil
		}
		return decorationsMsg{items: items}
	}
}

// updateViewportCmd fires an UpdateViewport RPC so the server knows where the
// client is scrolled. Fire-and-forget: no message is returned on completion.
func (m Model) updateViewportCmd() tea.Cmd {
	if m.rpc == nil {
		return nil
	}
	topLine := uint32(m.topLine)
	height := uint32(m.height)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		m.rpc.UpdateViewport(ctx, topLine, height)
		return nil
	}
}

func (m Model) fetchHover() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := m.rpc.Hover(ctx, bufID, line, col)
		if err != nil {
			return errorMsg{err}
		}
		return hoverMsg{result}
	}
}

func (m Model) fetchSignatureHelp() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		sh, err := m.rpc.SignatureHelp(ctx, bufID, line, col)
		if err != nil || len(sh.Signatures) == 0 {
			return sigHelpMsg{nil}
		}
		return sigHelpMsg{&sh}
	}
}

func (m Model) fetchCompletions() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		items, err := m.rpc.Complete(ctx, bufID, line, col)
		if err != nil {
			return nil
		}
		return completionsMsg{items}
	}
}

func (m Model) fetchDefinition() tea.Cmd {
	bufID := m.bufID
	line, col := m.cursor.Line, m.cursor.Col
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		loc, found, err := m.rpc.Definition(ctx, bufID, line, col)
		if err != nil {
			return nil
		}
		return definitionMsg{loc: loc, found: found}
	}
}

func (m Model) fetchFormat(thenSave bool) tea.Cmd {
	bufID := m.bufID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		content, changed, noFormatter, err := m.rpc.Format(ctx, bufID)
		if err != nil {
			return errorMsg{err}
		}
		return formatResultMsg{content: content, changed: changed, thenSave: thenSave, noFormatter: noFormatter}
	}
}

// currentWordPrefix returns the identifier fragment immediately before the cursor.
func (m Model) currentWordPrefix() string {
	line := m.buf.Line(m.cursor.Line)
	runes := []rune(line)
	col := min(m.cursor.Col, len(runes))
	start := col
	for start > 0 && isWordChar(runes[start-1]) {
		start--
	}
	return string(runes[start:col])
}

// diagsOnLine returns diagnostics on the given line, most severe first.
func (m Model) diagsOnLine(line int) []ClientDiag {
	var out []ClientDiag
	for _, d := range m.diagnostics {
		if d.Line == line {
			out = append(out, d)
		}
	}
	// Sort: lower severity number = more severe (1=error, 2=warn, ...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Severity < out[j-1].Severity; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
