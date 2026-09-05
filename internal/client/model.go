package client

import (
	"crypto/sha256"
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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
	ops        []document.Op
	version    uint64
	savedHash  []byte
	generation uint64
}

// errorMsg carries a non-fatal error to display in the status bar.
type errorMsg struct{ err error }

// RoutableMsg is implemented by async RPC-result messages whose bufID names
// the specific buffer they belong to, even when that buffer isn't the
// active tab. App.Update special-cases this interface to route the message
// directly to the owning buffer's Model (falling through to the generic
// active-buffer-only dispatch otherwise), instead of relying solely on each
// buffer's own handler discarding the message when its bufID doesn't match
// m.bufID: a bare discard silently drops the result for good if the
// originating buffer isn't active when it arrives, whereas routing lets an
// inactive buffer's save/discard/resync still take effect. Deliberately
// implemented by only a handful of message types (not every bufID-carrying
// one) — see each type's doc comment for why routing matters for it
// specifically.
type RoutableMsg interface {
	RouteBufID() uint32
}

// applyOpFailedMsg carries an ApplyOp RPC failure. Unlike other RPC errors
// (which just show a status message), this one means the local buffer has
// applied an edit the server never received — client and server have
// diverged. See applyOpFailed's handler for the hard-resync response.
// bufID is stamped at the point the failed op was queued and checked on
// arrival: without it, a slow ApplyOp for a buffer the user has since
// switched away from would resync whatever buffer now happens to be active
// (which never diverged) while leaving the buffer that actually failed
// silently corrupted. Implements RoutableMsg so App routes it to that
// buffer directly rather than dropping it when the buffer isn't active.
type applyOpFailedMsg struct {
	bufID uint32
	err   error
}

// RouteBufID implements RoutableMsg.
func (m applyOpFailedMsg) RouteBufID() uint32 { return m.bufID }

// saveFailedMsg carries a Save or SaveAs RPC failure. bufID is stamped at
// request time. Unlike a plain errorMsg (which the generic active-buffer-only
// dispatch shows on whatever tab happens to be active when the async result
// arrives), this is routed via RoutableMsg so a save failure is always shown
// on the buffer whose save actually failed — otherwise a user who switched
// tabs while a slow save failed could see the error on the wrong file, or
// never see it at all if they don't switch back before it's overwritten by
// another status update.
type saveFailedMsg struct {
	bufID uint32
	err   error
}

// RouteBufID implements RoutableMsg.
func (m saveFailedMsg) RouteBufID() uint32 { return m.bufID }

// discardRecoveryFailedMsg carries a DiscardRecovery RPC failure. bufID is
// stamped at request time and routed via RoutableMsg, same reasoning as
// saveFailedMsg. Additionally restores recoveryPrompt on the originating
// buffer: handleRecoveryPrompt already dismisses the prompt the instant the
// user presses "n" (before the RPC, up to 5s, even starts), so a failure
// here would otherwise leave the prompt gone with no way to retry the
// decision.
type discardRecoveryFailedMsg struct {
	bufID uint32
	err   error
}

// RouteBufID implements RoutableMsg.
func (m discardRecoveryFailedMsg) RouteBufID() uint32 { return m.bufID }

// bufferResyncMsg carries the result of re-fetching a buffer's authoritative
// content from the server after applyOpFailedMsg, to recover from the
// divergence rather than leaving it permanent and silent.
type bufferResyncMsg struct {
	bufID      uint32
	content    string
	version    uint64
	generation uint64
	path       string
	err        error
}

// savedMsg signals a successful save. bufID is stamped at request time and
// checked on arrival — otherwise a slow Save landing after the user switched
// tabs would clear the dirty flag on whatever buffer is now active instead
// of the one actually saved. version is m.buf.Version() at the moment the
// Save RPC was issued: bufID alone only catches "wrong buffer" — it says
// nothing about *this* buffer having been edited again (locally, or via a
// remote op arriving through updatesMsg) while the up-to-5s round trip was
// in flight. The server's own Save handler already detects that case and
// leaves its copy dirty (see server_buffer.go's compare-and-swap), so
// unconditionally calling SetClean() here on a version mismatch would
// disagree with the server and falsely mark newer, unsaved edits as saved.
// Implements RoutableMsg so App routes it to that buffer directly rather
// than dropping it when the buffer isn't active.
type savedMsg struct {
	bufID   uint32
	version uint64
}

// RouteBufID implements RoutableMsg.
func (m savedMsg) RouteBufID() uint32 { return m.bufID }

// savedAsMsg signals a successful save-as; carries the new path. bufID is
// stamped at request time and checked on arrival: without it, a slow SaveAs
// landing after the user switched to a different buffer would silently
// repoint that other (unrelated) buffer's identity at the new path and mark
// it clean — hiding its real unsaved changes behind the wrong filename, and
// corrupting whatever a later save of it writes to. version is m.buf.Version()
// at request time, same reasoning as savedMsg's: bufID matching isn't enough
// when the buffer itself was edited again during the round trip. Implements
// RoutableMsg so App routes it to that buffer directly rather than dropping
// it when the buffer isn't active.
type savedAsMsg struct {
	bufID     uint32
	version   uint64
	newPath   string
	thenClose bool
}

// RouteBufID implements RoutableMsg.
func (m savedAsMsg) RouteBufID() uint32 { return m.bufID }

// discardRecoveryMsg carries original file content after the server discards
// the recovery file. bufID is stamped at request time and checked on
// arrival — otherwise a slow DiscardRecovery landing after the user switched
// buffers would replace whatever buffer is now active with this buffer's
// discarded original content. version is m.buf.Version() at request time:
// the recoveryPrompt overlay only blocks keys while it's shown, and it's
// dismissed immediately on "n" — before the RPC round trip (up to 5s)
// completes — so the user can keep editing the *same* buffer in that
// window; applying msg.content then would silently discard those edits.
// Implements RoutableMsg so App routes it to that buffer directly rather
// than dropping it when the buffer isn't active.
type discardRecoveryMsg struct {
	bufID   uint32
	version uint64
	content string
}

// RouteBufID implements RoutableMsg.
func (m discardRecoveryMsg) RouteBufID() uint32 { return m.bufID }

// diagnosticsMsg carries fresh diagnostics from the server.
type diagnosticsMsg struct {
	bufID    uint32
	diags    []ClientDiag
	lspReady bool // true only when the LSP client process is actually running
}

// workspaceDiagSummaryMsg carries a fresh workspace-wide diagnostic summary
// from the server (see RPC.GetWorkspaceDiagnosticsSummary). Not bufID-scoped
// — it's the same project-wide count regardless of which buffer is active,
// so there's no staleness check to make on arrival, unlike diagnosticsMsg.
type workspaceDiagSummaryMsg struct {
	summary WorkspaceDiagnosticsSummary
}

// hoverMsg carries a hover result.
type hoverMsg struct{ result ClientHoverResult }

// sigHelpMsg carries a signature-help result (nil Signatures = dismiss).
type sigHelpMsg struct{ help *ClientSigHelp }

// pluginBindingsMsg delivers fresh plugin key bindings for the help popup.
type pluginBindingsMsg struct{ bindings []ClientPluginBinding }

// fixItemsMsg carries fix suggestions and/or context-sensitive actions for the F popup.
// bufID/at/version are stamped at request time in fetchFixes and checked on
// arrival so a slow response for a since-abandoned buffer/cursor position
// doesn't pop up showing fixes for wherever the request happened to be
// made, not where the cursor is now. version catches a narrower case at/
// bufID alone can't: the cursor returning to the exact requested position
// after an intervening edit (e.g. type a character, then undo it) — the
// buffer's content changed and reverted, but a stale LSP action's edits
// still encode positions computed against the original content, so
// applying it could silently corrupt text or land somewhere nonsensical.
type fixItemsMsg struct {
	items   []ClientFixItem
	decor   *ClientDecoration // nil when items come only from action providers
	bufID   uint32
	at      document.Pos
	version uint64
}

// completionsMsg carries fresh completion items.
type completionsMsg struct{ items []ClientCompletion }

// lspOverlayRefreshMsg fires after the post-edit debounce delay to re-fetch
// semantic tokens/inlay hints. seq is the lspOverlaySeq captured when it was
// scheduled; a stale seq (a newer edit has since invalidated the caches
// again) is ignored so a burst of edits causes one fetch, not one per edit.
type lspOverlayRefreshMsg struct{ seq int }

// inlayHintsMsg carries fresh inlay hints for the requested viewport.
type inlayHintsMsg struct {
	bufID uint32
	items []ClientInlayHint
}

// semanticTokensMsg carries fresh semantic tokens for the requested viewport.
type semanticTokensMsg struct {
	bufID uint32
	items []ClientSemanticToken
}

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

// organizeImportsMsg carries the result of an LSP "source.organizeImports"
// request. bufID is checked against the current buffer before applying, so
// a result that arrives after the user switched tabs/buffers is discarded
// instead of mutating whatever buffer is now active. bufVersion is checked
// too: the edit positions were computed by the language server against the
// buffer's content at request time, so even a same-buffer edit (local, or a
// remote one arriving via updatesMsg) that lands before the response does
// invalidates those coordinates just as much as switching buffers would —
// document.Buffer.Version() increments on every Apply, local or remote, so
// comparing it catches both.
type organizeImportsMsg struct {
	bufID      uint32
	bufVersion uint64
	edits      []ClientLspEdit
	err        error
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
	bufID       uint32
	content     string
	changed     bool
	thenSave    bool
	noFormatter bool
	// generation is only meaningful when changed is true — see RPC.Format's
	// doc comment. The formatResultMsg handler must adopt it into
	// m.generation on that path, or the next updatesMsg poll spuriously
	// treats this client's own format-on-save as a foreign buffer swap and
	// triggers an unnecessary resync (with its user-facing severe-error
	// modal).
	generation uint64
	err        error // non-nil on an RPC failure; every other field is zero when set
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

// OpenNewFileMsg signals the App to open the "New File" filename prompt.
type OpenNewFileMsg struct{}

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
// Include/Exclude optionally restrict which files are searched (e.g.
// "*.go", "src/"), each a space-separated list of glob patterns.
type GrepMsg struct {
	Pattern string
	Include string
	Exclude string
}

// OpenDiagnosticBrowserMsg signals the App to open the workspace diagnostic
// browser (currently open buffers only — see PLAN.md's workspace-scan
// follow-up for extending this to unopened files).
type OpenDiagnosticBrowserMsg struct{}

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

// pluginKeyResultMsg carries the result of a plugin key RPC call. bufID is
// the buffer the request was made against (captured client-side at request
// time, same as inlayHintsMsg/semanticTokensMsg), used to discard a result
// that arrives after the user has switched to a different buffer.
type pluginKeyResultMsg struct {
	bufID  uint32
	result PluginKeyResult
}

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
	seq      uint64 // Model.hlSeq's value when this request was issued; see reparseHighlight
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

// normalAccentColor is Normal mode's status bar label color. Fixed rather
// than theme-derived: Normal's cursor is reverse-video (no fixed color of
// its own to match against), so unlike Insert there's no per-theme value to
// pull it from yet.
//
// insertAccentColor is the pre-theme-load fallback for Insert's label and
// cursor color (see applyDefaultDark), used only before the real config
// theme is applied at startup. Once ApplyTheme runs, both are driven by
// theme.UI.InsertCursorBg instead — see [theme.UI.InsertCursorBg].
const (
	normalAccentColor = "#FFFFFF"
	insertAccentColor = "#AAFFAA"
)

// UI styles — initialized to default-dark values; replaced by ApplyTheme before first render.
var (
	barStyle        lipgloss.Style
	normalModeStyle lipgloss.Style
	insertModeStyle lipgloss.Style

	// cursorStyle is the style actually used to render the buffer cursor —
	// View() picks it from normalCursorStyle/insertCursorStyle each frame
	// based on m.mode, so the many cursor call sites (render.go,
	// multicursor.go) that read this global don't need mode threaded through
	// their signatures.
	cursorStyle       lipgloss.Style
	normalCursorStyle lipgloss.Style
	insertCursorStyle lipgloss.Style
	// Underline variants of the two above, used when config's cursor_shape is
	// "underline". Built here rather than derived at render time because the
	// accent colors they need come from the theme, which only ApplyTheme sees.
	// The per-mode color distinction is deliberately preserved across both
	// shapes, so picking a shape never costs the modal feedback.
	normalCursorUnderlineStyle lipgloss.Style
	insertCursorUnderlineStyle lipgloss.Style

	popupBg          color.Color
	popupBorderStyle lipgloss.Style
	popupKeyStyle    lipgloss.Style
	popupTextStyle   lipgloss.Style

	selectionStyle   lipgloss.Style
	gutterStyle      lipgloss.Style
	gutterCurStyle   lipgloss.Style
	flashGutterStyle lipgloss.Style
	indentGuideStyle lipgloss.Style
	rulerStyle       lipgloss.Style
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
	// normalModeStyle/insertModeStyle use the fixed accent colors below rather
	// than t.UI.NormalModeFg — normal mode has no separate cursor color to
	// match (its cursor is reverse-video), so its label isn't themeable yet.
	normalModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(normalAccentColor)).Bold(true)
	insertModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(t.UI.InsertCursorBg)).Bold(true)
	normalCursorStyle = lipgloss.NewStyle().Reverse(true)
	insertCursorStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.UI.InsertCursorBg)).Foreground(lipgloss.Color("#000000"))
	normalCursorUnderlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color(normalAccentColor))
	insertCursorUnderlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color(t.UI.InsertCursorBg))
	cursorStyle = normalCursorStyle

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
	rulerStyle = lipgloss.NewStyle().Background(lipgloss.Color("#303030"))

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
	normalModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(normalAccentColor)).Bold(true)
	insertModeStyle = lipgloss.NewStyle().Background(barBg).Foreground(lipgloss.Color(insertAccentColor)).Bold(true)
	normalCursorStyle = lipgloss.NewStyle().Reverse(true)
	insertCursorStyle = lipgloss.NewStyle().Background(lipgloss.Color(insertAccentColor)).Foreground(lipgloss.Color("#000000"))
	normalCursorUnderlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color(normalAccentColor))
	insertCursorUnderlineStyle = lipgloss.NewStyle().Underline(true).Foreground(lipgloss.Color(insertAccentColor))
	cursorStyle = normalCursorStyle

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
	rulerStyle = lipgloss.NewStyle().Background(lipgloss.Color("#303030"))

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
	pos     document.Pos
	sel     *Selection
	goalCol int // per-cursor sticky column, mirrors Model.goalCol; -1 when unset
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
	rpc             *RPC
	buf             *document.Buffer
	cfg             *config.Config
	bufID           uint32
	version         uint64
	generation      uint64 // last-known buffer generation; see updatesMsg's handler
	generationKnown bool   // false until the first updatesMsg/bufferResyncMsg establishes a baseline
	mode            Mode
	cursor          document.Pos
	goalCol         int  // sticky column for consecutive Up/Down moves; -1 when unset
	goalColActive   bool // set by moveCursor's vertical branch; tells the Update() dispatcher whether to keep goalCol
	topLine         int  // first visible line
	topChunk        int  // first visible wrap chunk of topLine (0-based)
	width           int
	height          int
	filePath        string
	workDir         string    // project root, used for display-path shortening
	status          string    // transient status/error message shown in modeline or, for error-class text, the toast overlay (see isErrMessage)
	statusAt        time.Time // when status was last set; drives toast auto-dismiss
	// keepStatusOnNextSave, when true, tells the next savedMsg handler to
	// skip its usual pushStatus("") clear once, then reset itself to false.
	// Set by formatResultMsg when format-on-save (thenSave) found nothing
	// to format: without this, the "No formatter available"/"Already
	// formatted" status it pushes would be silently wiped out by the save
	// that follows immediately after in the same flow, before the user
	// could ever see it — an explicit flag rather than a timing check,
	// matching this codebase's aversion to timing-based state.
	keepStatusOnNextSave bool
	severeErr            string // non-empty = must-dismiss error modal is visible (see handleKey); state-affecting failures only
	sel                  *Selection
	dragging             bool
	lastClickAt          time.Time
	lastClickPos         document.Pos
	undoStack            []undoEntry    // each entry is inverse ops + pre-edit cursor snapshot
	redoStack            []undoEntry    // mirrors undoStack; cleared on any new edit
	currentGroup         []document.Op  // non-nil while accumulating ops for the current Insert session
	groupBefore          cursorSnapshot // cursor state when currentGroup was opened
	insertLineCount      int            // buf line count at the start of the insert session
	savedUndoDepth       int            // len(undoStack) at the time of the last save
	cmdBuf               string         // text typed after ':' while in ModeCommand
	cmdCompletionIdx     int            // selected item in command completion popup (−1 = none)
	diagPopup            bool           // when true, show diagnostic detail popup for cursor line
	diagPopupSuppressed  bool           // Escape pressed; don't re-show until cursor leaves the range
	prefixSeq            []string       // keys typed so far for a multi-key Normal-mode command
	searchQuery          string         // raw text typed after '/' (see splitSearchQuery)
	searchReplace        string         // parsed replacement text, only meaningful when searchReplacing
	searchReplacing      bool           // true once an unescaped '/' delimiter has been typed — live search-and-replace preview
	searchMatches        []substituteMatch
	searchIdx            int
	searchOrigin         document.Pos
	searchErr            string // non-empty when regex fails to compile
	// lastSearchQuery is the pattern half (see splitSearchQuery) of the most
	// recently active search, kept even after withClearedSearch wipes
	// searchQuery/searchMatches — so n/N in Normal mode can revive a search
	// that's since been cleared (rather than being a no-op) by re-running
	// this pattern against the buffer's current content and jumping to its
	// first match. See withClearedSearch and reactivateLastSearch.
	lastSearchQuery string
	hlr             *highlight.Highlighter
	hlSpans         highlight.LineSpans
	// hlSeq is a pointer (like buf) so every value-copy of this buffer's
	// Model shares one counter: reparseHighlight bumps it and stamps the new
	// value onto the highlightMsg it schedules, so a highlightMsg superseded
	// by a later reparseHighlight call (e.g. a slow parse still in flight
	// when ":set ft=" issues a new one) can be recognized as stale and
	// discarded instead of clobbering newer spans. nil (as in tests that
	// build a Model{} literal directly rather than via New) disables the
	// check rather than panicking — every highlightMsg is then treated as
	// current, matching the pre-sequencing behavior.
	hlSeq          *uint64
	langOverride   string                 // set by ":set ft=<key>"; "" means derive language from filePath as usual
	detectedIndent *config.IndentSettings // sniffed from buffer content on open; nil if inconclusive
	metrics        *metricsData
	recoveryPrompt bool // waiting for user to accept or discard recovery content

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

	// Workspace-wide diagnostic summary, for the status bar's separate
	// project-scope indicator (distinct from the per-buffer E/W/I slots
	// above). Updated far less often than per-buffer diagnostics since it's
	// driven by an infrequent workspace lint scan, not live edits.
	workspaceDiagSummary WorkspaceDiagnosticsSummary
	workspaceDiagTick    int  // counter; fetch every 50 ticks (~6s)
	helpVisible          bool // true = help popup visible
	helpScroll           int  // scroll offset within the help popup

	// Message log (space l): every status-bar message since the buffer was
	// opened, so a message that scrolled off or got truncated in the status
	// bar can still be reviewed.
	messageLog    []logEntry
	msgLogVisible bool
	msgLogScroll  int

	hoverContent     *string            // non-nil = hover popup visible
	hoverScroll      int                // scroll offset within the hover popup
	hoverTotalLines  int                // total rendered body lines; used to clamp scroll
	sigHelp          *ClientSigHelp     // non-nil = signature help popup visible
	completions      []ClientCompletion // filtered/sorted view shown in the popup
	completionsRaw   []ClientCompletion // full unfiltered list from the last fetch
	completionOn     bool
	completionIdx    int
	completionPrefix string
	completionSeq    int // debounce token; only the latest auto-trigger fetches

	// Snippet (argument-placeholder) mode: after accepting a callable
	// completion, the inserted parameter names become tab stops the user can
	// jump between (Tab/Shift+Tab) and type over. Single line only.
	snippetOn    bool
	snippetLine  int
	snippetStops []snippetStop
	snippetIdx   int

	inlayHints []ClientInlayHint // current viewport's hints, refreshed periodically
	inlayTick  int               // counter; fetch every 10 ticks (~1.2s), same cadence as diagnostics

	semanticSpans highlight.LineSpans // current viewport's semantic-token spans, refreshed periodically
	semanticTick  int                 // counter; fetch every 10 ticks (~1.2s), same cadence as diagnostics
	lspOverlaySeq int                 // debounce token; only the latest post-edit refresh fetches

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

	// Macro recording: a single, in-memory-only slot (no named registers,
	// no persistence — see the design discussion this followed). q toggles
	// recording; every key seen by Update while macroRecording is true gets
	// appended to macroKeys, except the q keypresses that started/stopped
	// the recording itself (Update's tea.KeyMsg case excludes those by
	// comparing macroRecording before/after handleKey). @ replays lastMacro.
	macroRecording bool
	macroKeys      []tea.KeyMsg
	lastMacro      []tea.KeyMsg
	// macroReplayBlocked is set by executeMacroReplay when it refuses a @
	// press because a recording is in progress, and read (and cleared) by
	// Update's tea.KeyMsg case right after — see that case for why: without
	// it, the rejected @ would itself get recorded, and replaying the
	// finished macro would then re-invoke @ (now recording-free, so it
	// actually runs), which schedules another full replay of the same
	// macro — containing the same @ — recursing without end.
	macroReplayBlocked bool

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
	m = m.pushStatus(strings.Join(applyKeybindOverrides(cfg), "; "))
	return m
}

// New creates a Model after the buffer is already open with the server.
// generation is OpenFile's reported buffer generation, establishing the
// swap-detection baseline immediately rather than waiting for the first
// updatesMsg poll (see the generation field's doc comment).
func New(rpc *RPC, bufID uint32, content string, version uint64, filePath, workDir string, cfg *config.Config, fromRecovery bool, generation uint64) Model {
	buf := document.New(filePath, content)
	if fromRecovery {
		buf.MarkDirty()
	}
	warnings := applyKeybindOverrides(cfg)
	status := ""
	if len(warnings) > 0 {
		status = strings.Join(warnings, "; ")
	}
	m := Model{
		rpc:                 rpc,
		buf:                 buf,
		cfg:                 cfg,
		status:              status,
		bufID:               bufID,
		version:             version,
		generation:          generation,
		generationKnown:     true,
		filePath:            filePath,
		workDir:             workDir,
		hlr:                 highlight.New(filePath),
		hlSeq:               new(uint64),
		detectedIndent:      config.DetectIndentSettings(content),
		metrics:             &metricsData{},
		recoveryPrompt:      fromRecovery,
		pluginBindings:      rpc.PluginBindings(),
		reservePluginGutter: rpc != nil,
		lastReportedLine:    -1,
		lastReportedCol:     -1,
		goalCol:             -1,
	}
	// filePath's extension didn't resolve to a language (most commonly an
	// extension-less script) — fall back to sniffing a "#!" shebang, same
	// as ":set ft=<key>" would apply manually.
	if m.hlr == nil {
		if key := highlight.ShebangKey(content); key != "" {
			m = m.WithLangOverride(key)
		}
	}
	return m
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

// maxMessageLog caps how many status messages the message-log popup retains.
const maxMessageLog = 300

// toastDuration is how long an error-class status message (see isErrMessage)
// stays visible as the toast overlay before the tickMsg handler clears it.
// Non-error status messages have no expiry — they persist until replaced,
// same as before this existed.
const toastDuration = 6 * time.Second

// logEntry records one status-bar message for the message-log popup (space l).
type logEntry struct {
	at    time.Time
	text  string
	isErr bool
}

// isErrMessage reports whether a status/log message follows the "E: "/"ERR: "
// error convention used throughout this package. Error-class text renders as
// the auto-dismissing toast overlay (see View()) instead of the status-bar
// center segment, since that segment truncates long messages and is easy to
// miss once attention moves elsewhere.
func isErrMessage(text string) bool {
	return strings.HasPrefix(text, "E:") || strings.HasPrefix(text, "ERR:")
}

// appendMessageLog appends an entry to messageLog for later review (space l),
// trimming to the most recent maxMessageLog entries.
func (m Model) appendMessageLog(text string, isErr bool) Model {
	m.messageLog = append(m.messageLog, logEntry{at: time.Now(), text: text, isErr: isErr})
	if len(m.messageLog) > maxMessageLog {
		m.messageLog = append([]logEntry(nil), m.messageLog[len(m.messageLog)-maxMessageLog:]...)
	}
	return m
}

// pushStatus sets the transient status-bar/toast message and, unless it's a
// clear (text == ""), appends it to messageLog for later review.
func (m Model) pushStatus(text string) Model {
	m.status = text
	if text == "" {
		return m
	}
	m.statusAt = time.Now()
	return m.appendMessageLog(text, isErrMessage(text))
}

// pushSevereError shows a must-dismiss error modal (see handleKey's severeErr
// gate and renderSevereErrorPopup) instead of the toast pushStatus's
// error-class text would otherwise use. Reserved for failures that leave the
// buffer's state in question — an edit that failed to reach the server, or a
// resync whose outcome the user needs to consciously check — where silently
// auto-dismissing or letting a later status overwrite it risks the user
// never noticing. Still recorded in messageLog like any other message.
func (m Model) pushSevereError(text string) Model {
	m.severeErr = text
	return m.appendMessageLog(text, true)
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

// LangOverride returns the language key set by a prior ":set ft=<key>"
// (see the langOverride field doc comment), or "" if the language is still
// derived from filePath as usual. Used by app.doReloadBuffer to carry the
// override across an external-change reload, which otherwise rebuilds the
// Model from scratch via New and would silently lose it.
func (m Model) LangOverride() string { return m.langOverride }

// WithLangOverride reapplies a language override previously reported by
// LangOverride to m, exactly as ":set ft=<lang>" would (see keys_command.go)
// — used to restore it onto a Model rebuilt from scratch (see LangOverride's
// doc comment). A no-op if lang is "" or no longer resolves to a known
// highlighter (the latter shouldn't happen in practice: lang was already
// validated once by the ":set ft=" command that produced it).
func (m Model) WithLangOverride(lang string) Model {
	if lang == "" {
		return m
	}
	hlr := highlight.NewForKey(lang)
	if hlr == nil {
		return m
	}
	m.langOverride = lang
	m.hlr = hlr
	return m
}

// AtLine moves the initial cursor to the given 0-based line number.
func (m Model) AtLine(line int) Model {
	line = max(0, min(line, m.buf.LineCount()-1))
	m.cursor = document.Pos{Line: line, Col: 0}
	m.goalCol = -1
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
	m.goalCol = -1
	quarter := max(1, bufHeight/4)
	m.topLine = max(0, line-quarter)
	m.topChunk = 0
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
	m.goalCol = -1
	quarter := max(1, bufHeight/4)
	m.topLine = max(0, line-quarter)
	m.topChunk = 0
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
	return tea.Batch(tick(), m.reparseHighlight(), m.fetchDecorations(), m.fetchInlayHints(), m.fetchSemanticTokens(), m.fetchWorkspaceDiagnosticsSummary(), m.ReportActiveContextCmd())
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// See the matching guard in app.Update: tea.KeyMsg is an interface that
	// KeyReleaseMsg also satisfies, so the `case tea.KeyMsg` below would
	// otherwise run every binding a second time on release. Guarded here too
	// because Model.Update is driven directly (tests, and any embedding that
	// doesn't route through App).
	if _, isRelease := msg.(tea.KeyReleaseMsg); isRelease {
		return m, nil
	}

	switch msg := msg.(type) {

	case tea.PasteMsg:
		return m.handlePaste(msg.Content)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, m.updateViewportCmd()

	case tickMsg:
		if m.status != "" && isErrMessage(m.status) && time.Since(m.statusAt) > toastDuration {
			m.status = ""
		}
		m.diagTick++
		m.decorTick++
		m.inlayTick++
		m.semanticTick++
		m.workspaceDiagTick++
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
		if m.inlayTick%10 == 0 {
			cmds = append(cmds, m.fetchInlayHints())
		}
		if m.semanticTick%10 == 0 {
			cmds = append(cmds, m.fetchSemanticTokens())
		}
		if m.workspaceDiagTick%50 == 0 {
			cmds = append(cmds, m.fetchWorkspaceDiagnosticsSummary())
		}
		return m, tea.Batch(cmds...)

	case updatesMsg:
		if m.generationKnown && msg.generation != m.generation {
			// The server replaced this buffer's object wholesale since our
			// last known generation (format-on-save, SaveAs, DiscardRecovery,
			// explicit Format) — msg.ops describe changes to that different
			// object and can't be safely applied against our current one.
			m = m.pushStatus("Buffer changed on the server, resyncing...")
			return m, m.resyncFromServer()
		}
		m.generation = msg.generation
		m.generationKnown = true
		// Ops from other clients (agents, other windows) are undoable locally:
		// record inverses as a single undo entry so `u` reverts the whole batch.
		// GetUpdates never echoes this client's own ops back.
		before := m.cursorSnap()
		var inverses []document.Op
		atLine, delta := -1, 0
		for _, op := range msg.ops {
			if op.Type == document.OpInsert || op.Type == document.OpDelete {
				inverses = append(inverses, inverseOp(m, op)) // must precede Apply
			}
			al, d := opLineDelta(op)
			if atLine < 0 || al < atLine {
				atLine = al
			}
			delta += d
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
		m = m.shiftLSPOverlayLines(max(atLine, 0), delta)
		m, refreshCmd := m.scheduleLSPOverlayRefresh()
		return m, tea.Batch(m.reparseHighlight(), refreshCmd)

	case saveAsPromptMsg:
		s := m.filePath
		m.saveAsInput = &s
		m.saveAsThenClose = msg.thenClose
		return m, nil

	case errorMsg:
		m = m.pushStatus("ERR: " + msg.err.Error())
		return m, nil

	case applyOpFailedMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		m = m.pushSevereError("ERR: edit failed to reach server, resyncing: " + msg.err.Error())
		return m, m.resyncFromServer()

	case bufferResyncMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.err != nil {
			m = m.pushSevereError("ERR: resync failed, buffer may be out of sync with the server: " + msg.err.Error())
			return m, nil
		}
		if msg.path != "" && msg.path != m.filePath {
			m.filePath = msg.path
			if m.langOverride == "" {
				m.hlr = highlight.New(msg.path)
				if m.hlr == nil {
					if key := highlight.ShebangKey(msg.content); key != "" {
						m = m.WithLangOverride(key)
					}
				}
			}
		}
		m.buf = document.New(m.filePath, msg.content)
		m.buf.MarkDirty() // server's content may itself be unsaved-to-disk; err toward "unsaved" rather than a false-clean marker
		m.version = msg.version
		m.generation = msg.generation
		m.generationKnown = true
		m.undoStack = nil
		m.redoStack = nil
		m.currentGroup = nil
		m.sel = nil
		m.extraCursors = nil
		m.savedUndoDepth = -1 // never matches len(undoStack); only an explicit Save should clear dirty from here
		lc := m.buf.LineCount()
		if m.cursor.Line >= lc {
			m.cursor.Line = max(0, lc-1)
		}
		if lineLen := m.buf.LineLen(m.cursor.Line); m.cursor.Col > lineLen {
			m.cursor.Col = lineLen
		}
		m.scrollToCursor()
		m = m.pushSevereError("Buffer resynced from server — please check your last change")
		return m, m.reparseHighlight()

	case PluginShowMsgMsg:
		// Plugin messages show in the status bar (center segment).
		m = m.pushStatus(msg.Text)
		return m, nil

	case saveFailedMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		// A save that fails never reaches savedMsg to consume this flag —
		// reset it here too, so it can't leak and suppress an unrelated
		// later save's status clear.
		m.keepStatusOnNextSave = false
		m = m.pushStatus("ERR: " + msg.err.Error())
		return m, nil

	case savedMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.version != m.buf.Version() {
			// The buffer was edited again (locally, or via a remote op from
			// updatesMsg) while the save was in flight, so what's on disk no
			// longer matches the buffer's current content — the server's own
			// Save handler detects this exact race independently and leaves
			// its copy dirty (server_buffer.go's compare-and-swap). Marking
			// clean here would disagree with the server and falsely hide
			// newer, unsaved edits as saved.
			//
			// Reset keepStatusOnNextSave here too: this superseded response
			// never reaches the SetClean()/flag-consuming code below, so
			// without this the flag would leak and incorrectly protect a
			// later, unrelated save's status from being cleared — the same
			// leak class saveFailedMsg's handler already guards against.
			m.keepStatusOnNextSave = false
			return m, nil
		}
		m.buf.SetClean()
		if m.keepStatusOnNextSave {
			m.keepStatusOnNextSave = false
		} else {
			m = m.pushStatus("")
		}
		m.savedUndoDepth = len(m.undoStack)
		return m, nil

	case savedAsMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.version != m.buf.Version() {
			// Same reasoning as savedMsg: the buffer changed since the
			// SaveAs was issued, so newPath's content no longer matches the
			// buffer's current content. The server's own SaveAs handler
			// already rejects this in the common case (it returns an error,
			// which arrives as saveFailedMsg instead of this message) — this
			// covers the narrower window where the server's check passed but
			// a local edit landed before the response reached the client.
			//
			// Reset keepStatusOnNextSave here too, for the same reason
			// savedMsg's version-mismatch branch does: this superseded
			// response never reaches the flag-consuming code below.
			m.keepStatusOnNextSave = false
			m = m.pushStatus("Save as skipped — buffer has changed since the request; try again")
			return m, nil
		}
		m.filePath = msg.newPath
		m.saveAsThenClose = false
		m.buf.SetClean()
		if m.keepStatusOnNextSave {
			m.keepStatusOnNextSave = false
		} else {
			m = m.pushStatus("")
		}
		m.savedUndoDepth = len(m.undoStack)
		if msg.thenClose {
			return m, m.doCloseBuffer()
		}
		return m, nil

	case highlightMsg:
		if m.hlSeq != nil && msg.seq != *m.hlSeq {
			return m, nil // superseded by a newer reparseHighlight request; discard
		}
		m.hlSpans = msg.spans
		if m.metrics != nil {
			m.metrics.highlightDuration = msg.duration
		}
		return m, nil

	case discardRecoveryMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.version != m.buf.Version() {
			// recoveryPrompt is dismissed the instant "n" is pressed, before
			// the RPC (up to 5s) even starts, so the user could keep editing
			// this same buffer in the meantime. Applying msg.content now
			// would silently discard those edits — skip instead.
			m = m.pushStatus("Recovery discard skipped — buffer has changed since the request")
			return m, nil
		}
		m.buf = document.New(m.filePath, msg.content)
		m.version = 0
		m.undoStack = nil
		m.redoStack = nil
		m.currentGroup = nil
		m.savedUndoDepth = 0
		return m, m.reparseHighlight()

	case discardRecoveryFailedMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		// Restore the prompt so the user has a way to retry — it was
		// dismissed optimistically the instant "n" was pressed, before this
		// failure was known.
		m.recoveryPrompt = true
		m = m.pushSevereError("ERR: failed to discard recovery: " + msg.err.Error())
		return m, nil

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

	case workspaceDiagSummaryMsg:
		m.workspaceDiagSummary = msg.summary
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
			m = m.pushStatus("No hover info")
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
		// Drop a response that came back for a different buffer, after the
		// cursor moved, or after the buffer's content changed since the
		// request (even if the cursor ended up back at the same position —
		// see fixItemsMsg's doc comment): showing or applying it could pop
		// up (or silently apply) fixes computed against text that no longer
		// matches what's actually there.
		if msg.bufID != m.bufID || m.cursor != msg.at || msg.version != m.buf.Version() {
			m = m.pushStatus("Fixes unavailable (buffer changed)")
			return m, nil
		}
		if len(msg.items) > 0 {
			m.fixItems = msg.items
			m.fixDecor = msg.decor
			m.fixIdx = 0
		} else {
			m = m.pushStatus("No fixes available")
		}
		return m, nil

	case renameSymbolDoneMsg:
		switch {
		case msg.err != nil:
			m = m.pushStatus(fmt.Sprintf("E: rename failed: %v", msg.err))
		case msg.applied == 0:
			m = m.pushStatus("Rename: nothing to change (no language server for this file, or rename not supported)")
		default:
			m = m.pushStatus(fmt.Sprintf("Renamed %d occurrence(s) across %d file(s)", msg.applied, msg.files))
		}
		return m, nil

	case moveFunctionDoneMsg:
		if msg.err != nil {
			m = m.pushStatus(fmt.Sprintf("E: move failed: %v", msg.err))
		} else {
			m = m.pushStatus(fmt.Sprintf("Moved function to %s", msg.destPath))
		}
		return m, nil

	case organizeImportsMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.err == nil && len(msg.edits) > 0 && msg.bufVersion != m.buf.Version() {
			// The buffer changed (locally or via a remote op) since the
			// request was sent — the edits' positions no longer describe
			// the current content and must not be applied blindly.
			m = m.pushStatus("Organize Imports: buffer changed while waiting, discarded (try again)")
			return m, nil
		}
		switch {
		case msg.err != nil:
			m = m.pushStatus(fmt.Sprintf("E: organize imports failed: %v", msg.err))
			return m, nil
		case len(msg.edits) == 0:
			m = m.pushStatus("Organize Imports: nothing to change (no language server for this file, or not supported)")
			return m, nil
		default:
			m, cmd := applyLspEdits(m, msg.edits)
			return m.pushStatus("Organized imports"), cmd
		}

	case triggerCompletionMsg:
		if msg.seq != m.completionSeq {
			return m, nil // superseded by a later keystroke
		}
		return m, m.fetchCompletions()

	case formatResultMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		if msg.err != nil {
			m = m.pushStatus("ERR: " + msg.err.Error())
			return m, nil
		}
		var refreshCmd tea.Cmd
		if msg.changed {
			m.buf = document.New(m.filePath, msg.content)
			// document.New always starts a fresh buffer at version 0 (the
			// server's swapped-in buffer is the same fresh object — see
			// editor.capnp's generation doc comment), matching
			// discardRecoveryMsg's handler just above. Leaving m.version at
			// its pre-format value would let a later GetUpdates poll's
			// sinceVersion race ahead of the server's actual reset
			// version, so a real op from another client landing in that
			// window would look already-seen and never be delivered.
			m.version = 0
			m.generation = msg.generation
			m.generationKnown = true
			m.undoStack = nil
			m.redoStack = nil
			m.currentGroup = nil
			m.savedUndoDepth = 0
			m.cursor = document.Pos{
				Line: min(m.cursor.Line, m.buf.LineCount()-1),
			}
			m.scrollToCursor()
			if !msg.thenSave {
				m = m.pushStatus("Formatted")
			}
			// The formatter can move code arbitrarily, so the pre-format
			// tree-sitter spans and semantic-token/inlay-hint caches no
			// longer line up with anything — reparse now rather than
			// leaving stale highlighting on screen until an unrelated edit
			// happens to trigger a reparse.
			m.semanticSpans = nil
			m.inlayHints = nil
			m.detectedIndent = config.DetectIndentSettings(msg.content)
			m, refreshCmd = m.scheduleLSPOverlayRefresh()
			refreshCmd = tea.Batch(m.reparseHighlight(), refreshCmd)
		} else {
			if msg.noFormatter {
				m = m.pushStatus("No formatter available")
			} else {
				m = m.pushStatus("Already formatted")
			}
			// On format-on-save specifically, this status would otherwise be
			// invisible: the save that runs right after (below) completes
			// and clears it via savedMsg's own pushStatus("") before the
			// user ever sees it. The manual ":format" path (thenSave false)
			// needs no such protection — nothing clears its status right
			// after.
			if msg.thenSave {
				m.keepStatusOnNextSave = true
			}
		}
		if msg.thenSave {
			return m, tea.Batch(refreshCmd, m.doSaveNow())
		}
		return m, refreshCmd

	case definitionMsg:
		if !msg.found {
			m = m.pushStatus("No definition found")
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
			m = m.pushStatus("No references found")
			return m, nil
		}
		refs := msg.refs
		return m, func() tea.Msg { return OpenRefPickerMsg{Title: "References", Refs: refs} }

	case docSymbolsMsg:
		if len(msg.syms) == 0 {
			m = m.pushStatus("No symbols found")
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
		// The severe-error modal must block all input until explicitly
		// dismissed (see handleKey's severeErr gate) — mouse events bypass
		// handleKey entirely, so they need their own guard here.
		if m.severeErr != "" {
			return m, nil
		}
		prevTopLine := m.topLine
		prevCursor := m.cursor
		prevSel := copySel(m.sel)
		// v2 encodes the action in the message type rather than an Action
		// field, so this is a type switch over the concrete event types
		// behind the tea.MouseMsg interface. mouse carries the shared
		// {X, Y, Button, Mod} payload.
		mouse := msg.Mouse()
		switch msg.(type) {
		case tea.MouseWheelMsg:
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.scrollWheel(-wheelScrollLines)
			case tea.MouseWheelDown:
				m.scrollWheel(wheelScrollLines)
			}
		case tea.MouseClickMsg:
			if mouse.Button == tea.MouseLeft {
				m.handleMousePress(mouse.X, mouse.Y)
			}
		case tea.MouseMotionMsg:
			if m.dragging {
				m.handleMouseDrag(mouse.X, mouse.Y)
			}
		case tea.MouseReleaseMsg:
			if mouse.Button == tea.MouseLeft {
				m.dragging = false
				// A press+release on the same spot is just a cursor move; clear selection.
				if m.sel != nil && m.sel.Anchor == m.sel.Head {
					m.sel = nil
				}
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

	case lspOverlayRefreshMsg:
		if msg.seq != m.lspOverlaySeq {
			return m, nil // superseded by a later edit
		}
		return m, tea.Batch(m.fetchSemanticTokens(), m.fetchInlayHints())

	case inlayHintsMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		m.inlayHints = msg.items
		return m, nil

	case semanticTokensMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
		m.semanticSpans = buildSemanticSpans(msg.items)
		return m, nil

	case pluginKeyResultMsg:
		if msg.bufID != m.bufID {
			return m, nil // stale result from a previous buffer switch; discard
		}
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

	case PluginDecorationsChangedMsg:
		if msg.BufID != m.bufID {
			return m, nil // not the buffer this window is viewing
		}
		return m, m.fetchDecorations()

	case tea.KeyMsg:
		if m.metrics != nil {
			m.metrics.lastKeyAt = time.Now()
		}
		prevLine := m.cursor.Line
		prevTopLine := m.topLine
		prevSel := copySel(m.sel)
		wasRecording := m.macroRecording
		m.goalColActive = false
		newModel, cmd := m.handleKey(msg)
		nm, ok := newModel.(Model)
		if !ok {
			return newModel, cmd
		}
		// Record this key iff recording was already active both before and
		// after handling it — that excludes exactly the two q keypresses
		// that start/stop a recording (nm.macroRecording flips relative to
		// wasRecording for those), while still capturing every other key
		// across any mode a recording spans (insert, search, popups, ...).
		// A Normal-mode @ that executeMacroReplay rejected because a
		// recording is in progress is excluded too (see macroReplayBlocked's
		// doc comment) — everywhere else (Insert/Search/Command mode, where
		// @ is never bound to replay at all) it's just a literal character
		// and is recorded normally.
		if nm.macroReplayBlocked {
			nm.macroReplayBlocked = false
		} else if wasRecording && nm.macroRecording {
			nm.macroKeys = append(nm.macroKeys, msg)
		}
		// Only a vertical move (moveCursor's dLine!=0/dCol==0 branch) sets
		// goalColActive; any other key — horizontal movement, edits, jumps —
		// should forget the remembered column so the next Up/Down starts
		// fresh from wherever the cursor actually ended up.
		if !nm.goalColActive {
			nm.goalCol = -1
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
