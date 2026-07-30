package client

import (
	"crypto/sha256"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/document"
	"github.com/indiejames/indigo/internal/highlight"
	"github.com/indiejames/indigo/internal/theme"
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

// updatesMsg carries ops received from the server, plus the sha256 of the
// buffer content at its last save (for dirty-marker reconciliation).
type updatesMsg struct {
	ops       []document.Op
	version   uint64
	savedHash []byte
}

// errorMsg carries a non-fatal error to display in the status bar.
type errorMsg struct{ err error }

// savedMsg signals a successful save.
type savedMsg struct{}

// savedAsMsg signals a successful save-as; carries the new path.
type savedAsMsg struct {
	newPath   string
	thenClose bool
}

// discardRecoveryMsg carries original file content after the server discards the recovery file.
type discardRecoveryMsg struct{ content string }

// diagnosticsMsg carries fresh diagnostics from the server.
type diagnosticsMsg struct {
	bufID    uint32
	diags    []ClientDiag
	lspReady bool // true only when the LSP client process is actually running
}

// hoverMsg carries a hover result.
type hoverMsg struct{ result ClientHoverResult }

// sigHelpMsg carries a signature-help result (nil Signatures = dismiss).
type sigHelpMsg struct{ help *ClientSigHelp }

// pluginBindingsMsg delivers fresh plugin key bindings for the help popup.
type pluginBindingsMsg struct{ bindings []ClientPluginBinding }

// fixItemsMsg carries fix suggestions and/or context-sensitive actions for the F popup.
type fixItemsMsg struct {
	items []ClientFixItem
	decor *ClientDecoration // nil when items come only from action providers
}

// completionsMsg carries fresh completion items.
type completionsMsg struct{ items []ClientCompletion }

// completionResolvedMsg carries a completion item resolved on accept, along with
// the cursor position and typed prefix captured when the user accepted it, so
// the deferred apply lands at the right place regardless of the current cursor.
type completionResolvedMsg struct {
	item   ClientCompletion
	at     document.Pos
	prefix string
	bufID  uint32
}

// renameSymbolDoneMsg carries the result of an LSP-driven rename.
type renameSymbolDoneMsg struct {
	applied, files int
	err            error
}

// moveFunctionDoneMsg carries the result of moving a function to another file.
type moveFunctionDoneMsg struct {
	destPath string
	err      error
}

// triggerCompletionMsg fires after the auto-trigger debounce delay. seq is the
// completionSeq captured when it was scheduled; a stale seq (a newer keystroke
// has since been typed) is ignored so a burst of typing causes one fetch.
type triggerCompletionMsg struct{ seq int }

// definitionMsg carries the result of a go-to-definition request.
type definitionMsg struct {
	loc   ClientLocation
	found bool
}

// referencesMsg carries find-references results from the server.
type referencesMsg struct {
	refs []ClientReference
}

// docSymbolsMsg carries document symbol results.
type docSymbolsMsg struct {
	syms []ClientSymbol
}

// OpenSymbolPickerMsg signals the App to open the workspace symbol picker.
type OpenSymbolPickerMsg struct{ BufID uint32 }

// OpenDocSymbolPickerMsg signals the App to open the document symbol picker.
type OpenDocSymbolPickerMsg struct {
	BufID uint32
	Syms  []ClientSymbol
}

// OpenRefPickerMsg signals the App to open the find-references picker.
type OpenRefPickerMsg struct {
	Title string
	Refs  []ClientReference
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
// Col is the 0-based column; -1 means no specific column (use start of line).
type OpenFileAtMsg struct {
	Path string
	Line int
	Col  int
}

// OpenPickerMsg signals the App to open the file picker.
type OpenPickerMsg struct{}

// OpenSearchReplaceMsg signals the App to open the global search & replace dialog.
type OpenSearchReplaceMsg struct{}

// JumpBackMsg signals the App to jump to the previous edit position in the jump list.
type JumpBackMsg struct{}

// JumpForwardMsg signals the App to jump to the next edit position in the jump list.
type JumpForwardMsg struct{}

// EditRecordMsg signals the App to (a) adjust existing jump entries shifted by
// this edit and (b) add a new entry at {Line, Col}.
// AtLine / LineDelta describe the line-shift effect (LineDelta == 0 → no adjustment).
// UndoDepth is len(undoStack) after this edit is committed; used to prune entries on undo.
type EditRecordMsg struct {
	FilePath  string
	Line, Col int // new jump entry position
	AtLine    int // adjustment boundary
	LineDelta int // net lines added (>0) or removed (<0)
	UndoDepth int // undo stack depth at which this entry was created
}

// UndoMsg signals the App that an undo was performed in the given buffer.
// NewDepth is len(undoStack) after the undo; all jump entries with
// undoDepth > NewDepth are removed.
// AtLine and LineDelta describe the net line-count change caused by the undo
// operation itself (so existing jump entries can be re-adjusted).
type UndoMsg struct {
	FilePath  string
	NewDepth  int
	AtLine    int
	LineDelta int
}

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

// OpenBufPickerMsg signals the App to open the buffer picker popup.
type OpenBufPickerMsg struct{}

// QuitAllMsg signals the App to quit all buffers (:qa / :qa! / :wqa).
type QuitAllMsg struct {
	Force   bool // :qa! — skip dirty check
	SaveAll bool // :wqa — save dirty buffers first
}

// pluginKeyResultMsg carries the result of a plugin key RPC call.
type pluginKeyResultMsg struct{ result PluginKeyResult }

// decorationsMsg carries fresh plugin decorations from the server.
type decorationsMsg struct {
	bufID uint32
	items []ClientDecoration
}

// showDiagPopupMsg fires after the 300 ms auto-show delay.
type showDiagPopupMsg struct{}

// scheduleShowDiagPopup returns a command that delivers showDiagPopupMsg after 300 ms.
func scheduleShowDiagPopup() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(300 * time.Millisecond)
		return showDiagPopupMsg{}
	}
}

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

// UI styles — initialized to default-dark values; replaced by ApplyTheme before first render.
var (
	barStyle        lipgloss.Style
	normalModeStyle lipgloss.Style
	insertModeStyle lipgloss.Style
	cursorStyle     lipgloss.Style

	popupBg          lipgloss.Color
	popupBorderStyle lipgloss.Style
	popupKeyStyle    lipgloss.Style
	popupTextStyle   lipgloss.Style

	selectionStyle   lipgloss.Style
	gutterStyle      lipgloss.Style
	gutterCurStyle   lipgloss.Style
	flashGutterStyle lipgloss.Style
	indentGuideStyle lipgloss.Style
	flashPadStyle    lipgloss.Style

	diagErrorStyle lipgloss.Style
	diagWarnStyle  lipgloss.Style
	diagInfoStyle  lipgloss.Style

	barDiagErrorStyle lipgloss.Style
	barDiagWarnStyle  lipgloss.Style
	barDiagInfoStyle  lipgloss.Style

	fileTypeStyle  lipgloss.Style
	lspIdleStyle   lipgloss.Style
	lspActiveStyle lipgloss.Style

	// Hex colors for diagnostic underlines (updated by ApplyTheme).
	activeDiagError = "#FF5555"
	activeDiagWarn  = "#FFDD44"
	activeDiagInfo  = "#88AAFF"

	// Hex color for the matching-bracket/quote underline (updated by ApplyTheme).
	activeMatchPair = "#AAAAAA"

	// Popup border characters (updated by ApplyTheme).
	bdrTL       = "╭"
	bdrTR       = "╮"
	bdrBL       = "╰"
	bdrBR       = "╯"
	bdrH        = "─"
	bdrV        = "│"
	bdrLipgloss = lipgloss.RoundedBorder()
)

func init() {
	applyDefaultDark()
}

// ApplyTheme updates all UI styles and border characters from the given theme.
// Must be called before the first render (i.e. before tea.NewProgram).
func ApplyTheme(t *theme.Theme) {
	barBg := lipgloss.Color(t.UI.BarBg)
	darkBg := lipgloss.Color(t.UI.BarDarkBg)
	pb := lipgloss.Color(t.UI.PopupBg)

	barStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.BarFg))
	normalModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.NormalModeFg)).Bold(true)
	insertModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.InsertModeFg)).Bold(true)
	cursorStyle = lipgloss.NewStyle().Reverse(true)

	popupBg = pb
	popupBorderStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color(t.UI.PopupBorderFg))
	popupKeyStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color(t.UI.PopupKeyFg)).Bold(true)
	popupTextStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color(t.UI.PopupTextFg))

	selectionStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.UI.SelectionBg)).Foreground(lipgloss.Color(t.UI.SelectionFg))
	gutterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.UI.GutterFg))
	gutterCurStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.UI.GutterCurFg))
	flashGutterStyle = lipgloss.NewStyle().Background(lipgloss.Color("#097AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	flashPadStyle = lipgloss.NewStyle().Background(lipgloss.Color("#097AC8"))
	indentGuideStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#404040"))

	diagErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.UI.DiagErrorFg))
	diagWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.UI.DiagWarnFg))
	diagInfoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.UI.DiagInfoFg))

	barDiagErrorStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.DiagErrorFg))
	barDiagWarnStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.DiagWarnFg))
	barDiagInfoStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.DiagInfoFg))

	fileTypeStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color("#CCDDFF"))
	lspIdleStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color("#667788"))
	lspActiveStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color(t.UI.NormalModeFg))

	activeDiagError = t.UI.DiagErrorFg
	activeDiagWarn = t.UI.DiagWarnFg
	activeDiagInfo = t.UI.DiagInfoFg
	activeMatchPair = t.UI.MatchPairFg

	bc := t.BorderChars()
	bdrTL, bdrTR, bdrBL, bdrBR, bdrH, bdrV = bc[0], bc[1], bc[2], bc[3], bc[4], bc[5]
	switch t.UI.PopupBorder {
	case "square":
		bdrLipgloss = lipgloss.NormalBorder()
	case "double":
		bdrLipgloss = lipgloss.DoubleBorder()
	case "none":
		bdrLipgloss = lipgloss.HiddenBorder()
	default:
		bdrLipgloss = lipgloss.RoundedBorder()
	}
}

// applyDefaultDark initialises all styles to the built-in default-dark palette.
// Called from init() so tests and any code path that skips ApplyTheme still work.
func applyDefaultDark() {
	barBg := lipgloss.Color("#087AC8")
	barFg := lipgloss.Color("#FFFFFF")
	darkBg := lipgloss.Color("#065A96")
	pb := lipgloss.Color("#1E2A38")

	barStyle = lipgloss.NewStyle().Background(barBg).Foreground(barFg)
	normalModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color("#AAFFAA")).Bold(true)
	insertModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color("#AADDFF")).Bold(true)
	cursorStyle = lipgloss.NewStyle().Reverse(true)

	popupBg = pb
	popupBorderStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color("#4488CC"))
	popupKeyStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color("#FFDD44")).Bold(true)
	popupTextStyle = lipgloss.NewStyle().Background(pb).Foreground(lipgloss.Color("#CCDDEE"))

	selectionStyle = lipgloss.NewStyle().Background(lipgloss.Color("#2D5F8A")).Foreground(lipgloss.Color("#FFFFFF"))
	gutterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#606060"))
	gutterCurStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#AAAAAA"))
	flashGutterStyle = lipgloss.NewStyle().Background(lipgloss.Color("#097AC8")).Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	flashPadStyle = lipgloss.NewStyle().Background(lipgloss.Color("#097AC8"))
	indentGuideStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#404040"))

	diagErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5555"))
	diagWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFDD44"))
	diagInfoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#88AAFF"))

	barDiagErrorStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color("#FF8888"))
	barDiagWarnStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color("#FFDD44"))
	barDiagInfoStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color("#88AAFF"))

	fileTypeStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color("#CCDDFF"))
	lspIdleStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color("#667788"))
	lspActiveStyle = lipgloss.NewStyle().Background(darkBg).Foreground(lipgloss.Color("#AAFFAA"))

	activeDiagError = "#FF5555"
	activeDiagWarn = "#FFDD44"
	activeDiagInfo = "#88AAFF"
	activeMatchPair = "#AAAAAA"

	bdrTL, bdrTR, bdrBL, bdrBR, bdrH, bdrV = "╭", "╮", "╰", "╯", "─", "│"
	bdrLipgloss = lipgloss.RoundedBorder()
}

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
	rpc                 *RPC
	buf                 *document.Buffer
	cfg                 *config.Config
	bufID               uint32
	version             uint64
	mode                Mode
	cursor              document.Pos
	topLine             int // first visible line
	width               int
	height              int
	filePath            string
	workDir             string // project root, used for display-path shortening
	status              string // transient error message shown in modeline
	sel                 *Selection
	dragging            bool
	lastClickAt         time.Time
	lastClickPos        document.Pos
	undoStack           []undoEntry    // each entry is inverse ops + pre-edit cursor snapshot
	redoStack           []undoEntry    // mirrors undoStack; cleared on any new edit
	currentGroup        []document.Op  // non-nil while accumulating ops for the current Insert session
	groupBefore         cursorSnapshot // cursor state when currentGroup was opened
	insertLineCount     int            // buf line count at the start of the insert session
	savedUndoDepth      int            // len(undoStack) at the time of the last save
	cmdBuf              string         // text typed after ':' while in ModeCommand
	cmdCompletionIdx    int            // selected item in command completion popup (−1 = none)
	diagPopup           bool           // when true, show diagnostic detail popup for cursor line
	diagPopupSuppressed bool           // Escape pressed; don't re-show until cursor leaves the range
	prefixSeq           []rune         // keys typed so far for a multi-key Normal-mode command
	searchQuery         string
	searchMatches       []searchMatch
	searchIdx           int
	searchOrigin        document.Pos
	searchErr           string // non-empty when regex fails to compile
	hlr                 *highlight.Highlighter
	hlSpans             highlight.LineSpans
	metrics             *metricsData
	recoveryPrompt      bool // waiting for user to accept or discard recovery content

	// Plugin decorations
	decorations         []ClientDecoration
	decorTick           int  // poll every 3 ticks (~360ms)
	reservePluginGutter bool // latched true once gutter decorations have been seen; never resets

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
	diagTick    int  // counter; fetch every 10 ticks (~1.2s)
	lspActive   bool // true once first diagnostic poll returns (LSP is running)
	helpVisible bool // true = help popup visible
	helpScroll  int  // scroll offset within the help popup

	hoverContent     *string        // non-nil = hover popup visible
	hoverScroll      int            // scroll offset within the hover popup
	hoverTotalLines  int            // total rendered body lines; used to clamp scroll
	sigHelp          *ClientSigHelp // non-nil = signature help popup visible
	completions      []ClientCompletion // filtered/sorted view shown in the popup
	completionsRaw   []ClientCompletion // full unfiltered list from the last fetch
	completionOn     bool
	completionIdx    int
	completionPrefix string
	completionSeq    int // debounce token; only the latest auto-trigger fetches

	// Fix popup state (Shift+F)
	fixItems []ClientFixItem   // non-empty = popup visible
	fixDecor *ClientDecoration // decoration being fixed (nil for action-only items)
	fixIdx   int

	// pendingExtract holds a range-extract action's (Extract Function/
	// Extract Variable) edits while the user is prompted for the new
	// symbol's name on the command line; see startExtractRenamePrompt and
	// the "extract-rename " command-line prefix in executeCommand.
	pendingExtract *pendingExtractRename

	// Mark for deferred selection: set with z, select-to with Z.
	mark *document.Pos

	// Plugin help entries loaded at startup for the ? popup.
	pluginBindings []ClientPluginBinding

	// flashTick counts down after a jump (AtPos); the cursor line is highlighted
	// while it is odd, creating a brief alternating flash effect.
	flashTick int

	// lastReportedLine/Col track the cursor position last sent to the server via
	// SetActiveContext. Initialized to -1 so the first cursor move always reports.
	// Reset to -1 on blur so re-focus always triggers a fresh report.
	lastReportedLine int
	lastReportedCol  int
}

// WithConfig returns a copy of the model with a new config applied.
// Used for hot-reloading preferences at runtime.
func (m Model) WithConfig(cfg *config.Config) Model {
	m.cfg = cfg
	return m
}

// New creates a Model after the buffer is already open with the server.
func New(rpc *RPC, bufID uint32, content string, version uint64, filePath, workDir string, cfg *config.Config, fromRecovery bool) Model {
	buf := document.New(filePath, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	return Model{
		rpc:                 rpc,
		buf:                 buf,
		cfg:                 cfg,
		bufID:               bufID,
		version:             version,
		filePath:            filePath,
		workDir:             workDir,
		hlr:                 highlight.New(filePath),
		metrics:             &metricsData{},
		recoveryPrompt:      fromRecovery,
		pluginBindings:      rpc.PluginBindings(),
		reservePluginGutter: rpc != nil,
		lastReportedLine:    -1,
		lastReportedCol:     -1,
	}
}

// Dirty reports whether the buffer has unsaved changes.
func (m Model) Dirty() bool { return m.buf.Dirty() }

// clientID returns the RPC client ID, or 0 when rpc is nil (e.g. in tests).
func (m Model) clientID() uint64 {
	if m.rpc == nil {
		return 0
	}
	return m.rpc.ClientID()
}

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

// AtPos moves the cursor to the given 0-based (line, col), scrolls so the
// target line sits ~25% down from the top of the visible area, and starts
// a brief flash to make the landed line easy to spot.
func (m Model) AtPos(line, col, bufHeight int) Model {
	line = max(0, min(line, m.buf.LineCount()-1))
	col = max(0, min(col, m.buf.LineLen(line)))
	m.cursor = document.Pos{Line: line, Col: col}
	quarter := max(1, bufHeight/4)
	m.topLine = max(0, line-quarter)
	m.flashTick = 5
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
	return tea.Batch(tick(), m.reparseHighlight(), m.fetchDecorations(), m.ReportActiveContextCmd())
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
		if m.flashTick > 0 {
			m.flashTick--
		}
		cmds := []tea.Cmd{m.fetchUpdates(), tick()}
		if m.diagTick%10 == 0 {
			cmds = append(cmds, m.fetchDiagnostics())
		}
		if m.decorTick%3 == 0 {
			cmds = append(cmds, m.fetchDecorations())
		}
		return m, tea.Batch(cmds...)

	case updatesMsg:
		// Ops from other clients (agents, other windows) are undoable locally:
		// record inverses as a single undo entry so `u` reverts the whole batch.
		// GetUpdates never echoes this client's own ops back.
		before := m.cursorSnap()
		var inverses []document.Op
		for _, op := range msg.ops {
			if op.Type == document.OpInsert || op.Type == document.OpDelete {
				inverses = append(inverses, inverseOp(m, op)) // must precede Apply
			}
			m.buf.Apply(op)
		}
		if len(inverses) > 0 {
			m.undoStack = append(m.undoStack, undoEntry{ops: inverses, before: before})
			m.redoStack = nil
		}
		m.version = msg.version
		// Reconcile the dirty marker: if another client saved this buffer, our
		// content now matches disk exactly when its hash equals savedHash. The
		// hash check makes this race-free — an in-flight local keystroke means
		// the hashes differ, so a stale response can never mask dirtiness.
		if m.buf.Dirty() && len(msg.savedHash) == sha256.Size {
			if sha256.Sum256([]byte(m.buf.Content())) == [sha256.Size]byte(msg.savedHash) {
				m.buf.SetClean()
				m.savedUndoDepth = len(m.undoStack)
			}
		}
		if len(msg.ops) == 0 {
			return m, nil
		}
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

	case PluginShowMsgMsg:
		// Plugin messages show in the status bar (center segment).
		m.status = msg.Text
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
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		m.diagnostics = m.expandDiags(msg.diags)
		if msg.lspReady {
			m.lspActive = true
		}
		atCursor := len(m.diagsAtPos(m.cursor.Line, m.cursor.Col)) > 0
		if m.diagPopup && !atCursor {
			m.diagPopup = false
			m.diagPopupSuppressed = false
		}
		if !m.diagPopup && !m.diagPopupSuppressed && atCursor {
			return m, scheduleShowDiagPopup()
		}
		return m, nil

	case showDiagPopupMsg:
		if !m.diagPopupSuppressed && len(m.diagsAtPos(m.cursor.Line, m.cursor.Col)) > 0 {
			m.diagPopup = true
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

	case pluginBindingsMsg:
		m.pluginBindings = msg.bindings
		m.helpVisible = true
		m.helpScroll = 0
		return m, nil

	case sigHelpMsg:
		m.sigHelp = msg.help
		return m, nil

	case completionsMsg:
		// Recompute the prefix from the live buffer: the fetch was async and the
		// cursor may have advanced (auto-trigger debounce), so filter against
		// what's actually typed now, not what was typed when the fetch started.
		m.completionPrefix = m.currentWordPrefix()
		m.completionsRaw = msg.items
		m.completions = filterCompletions(msg.items, m.completionPrefix)
		if len(m.completions) == 0 {
			m.completionOn = false
			m.completionsRaw = nil
		} else {
			m.completionOn = true
			m.completionIdx = 0
		}
		return m, nil

	case completionResolvedMsg:
		// Drop a resolution that came back for a different buffer, or after the
		// cursor moved (the user kept typing or navigated): applying at the stale
		// position could target the wrong buffer or corrupt the text.
		if msg.bufID != m.bufID || m.cursor != msg.at {
			return m, nil
		}
		return m.applyCompletionItem(msg.item, msg.at, msg.prefix)

	case fixItemsMsg:
		if len(msg.items) > 0 {
			m.fixItems = msg.items
			m.fixDecor = msg.decor
			m.fixIdx = 0
		} else {
			m.status = "No fixes available"
		}
		return m, nil

	case renameSymbolDoneMsg:
		switch {
		case msg.err != nil:
			m.status = fmt.Sprintf("E: rename failed: %v", msg.err)
		case msg.applied == 0:
			m.status = "Rename: nothing to change (no language server for this file, or rename not supported)"
		default:
			m.status = fmt.Sprintf("Renamed %d occurrence(s) across %d file(s)", msg.applied, msg.files)
		}
		return m, nil

	case moveFunctionDoneMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("E: move failed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Moved function to %s", msg.destPath)
		}
		return m, nil

	case triggerCompletionMsg:
		if msg.seq != m.completionSeq {
			return m, nil // superseded by a later keystroke
		}
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
			m = m.AtPos(msg.loc.Line, msg.loc.Col, m.height)
			return m, nil
		}
		// Different file: ask the App to open it, carrying the column so it
		// also lands at 25% down rather than the top.
		loc := msg.loc
		return m, func() tea.Msg {
			return OpenFileAtMsg(loc)
		}

	case referencesMsg:
		if len(msg.refs) == 0 {
			m.status = "No references found"
			return m, nil
		}
		refs := msg.refs
		return m, func() tea.Msg { return OpenRefPickerMsg{Title: "References", Refs: refs} }

	case docSymbolsMsg:
		if len(msg.syms) == 0 {
			m.status = "No symbols found"
			return m, nil
		}
		syms := msg.syms
		bufID := m.bufID
		_ = bufID
		return m, func() tea.Msg { return OpenDocSymbolPickerMsg{BufID: bufID, Syms: syms} }

	case tea.FocusMsg:
		return m, m.ReportActiveContextCmd()

	case tea.BlurMsg:
		m.lastReportedLine = -1
		m.lastReportedCol = -1
		return m, nil

	case tea.MouseMsg:
		prevTopLine := m.topLine
		prevCursor := m.cursor
		prevSel := copySel(m.sel)
		switch {
		case msg.Button == tea.MouseButtonWheelUp:
			m.scrollWheel(-wheelScrollLines)
		case msg.Button == tea.MouseButtonWheelDown:
			m.scrollWheel(wheelScrollLines)
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
		var cmds []tea.Cmd
		if m.topLine != prevTopLine {
			cmds = append(cmds, m.updateViewportCmd())
		}
		reported := false
		if m.cursor != prevCursor && (m.cursor.Line != m.lastReportedLine || m.cursor.Col != m.lastReportedCol) {
			m.lastReportedLine = m.cursor.Line
			m.lastReportedCol = m.cursor.Col
			cmds = append(cmds, m.ReportActiveContextCmd())
			reported = true
		}
		// Selection changes without a cursor move (e.g. click clearing a
		// selection, drag extension) also report.
		if !reported && !selEqual(prevSel, m.sel) {
			cmds = append(cmds, m.ReportActiveContextCmd())
		}
		return m, tea.Batch(cmds...)

	case decorationsMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		m.decorations = msg.items
		if !m.reservePluginGutter {
			for _, d := range msg.items {
				if d.Kind == ClientDecorationGutter || d.Kind == ClientDecorationLeftGutter {
					m.reservePluginGutter = true
					break
				}
			}
		}
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
		prevLine := m.cursor.Line
		prevTopLine := m.topLine
		prevSel := copySel(m.sel)
		newModel, cmd := m.handleKey(msg)
		nm, ok := newModel.(Model)
		if !ok {
			return newModel, cmd
		}
		if nm.topLine != prevTopLine {
			cmd = tea.Batch(cmd, nm.updateViewportCmd())
		}
		// When the cursor moves, manage the diagnostic popup.
		if nm.cursor.Line != prevLine || nm.cursor.Col != m.cursor.Col {
			atPos := len(nm.diagsAtPos(nm.cursor.Line, nm.cursor.Col)) > 0
			if !atPos {
				// Left the diagnostic range: dismiss and clear suppression.
				nm.diagPopup = false
				nm.diagPopupSuppressed = false
			} else if nm.diagPopup {
				// Still in range with popup visible: keep it.
			} else if !nm.diagPopupSuppressed {
				// Entered a range (not suppressed): schedule show.
				cmd = tea.Batch(cmd, scheduleShowDiagPopup())
			}
		}
		// Fallback focus detection: if terminal focus events aren't working,
		// the first cursor move or edit after focus switches panes reports the
		// active context. Once reported, skip until BlurMsg clears the flag.
		// Selection changes (start, extend, clear — e.g. Esc) also report so
		// external tools never see a stale selection.
		needReport := false
		if nm.cursor.Line != nm.lastReportedLine || nm.cursor.Col != nm.lastReportedCol {
			nm.lastReportedLine = nm.cursor.Line
			nm.lastReportedCol = nm.cursor.Col
			needReport = true
		}
		if !selEqual(prevSel, nm.sel) {
			needReport = true
		}
		if needReport {
			cmd = tea.Batch(cmd, nm.ReportActiveContextCmd())
		}
		return nm, cmd
	}
	return m, nil
}
