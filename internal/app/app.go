package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

func appLog(format string, args ...any) {
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	fmt.Fprintf(f, "[app] "+format+"\n", args...) //nolint:errcheck
}

// appQuitMsg is returned by the cleanup cmd to trigger program exit.
type appQuitMsg struct{}

// openFileMsg asks the App to open a file by absolute path.
type openFileMsg struct{ absPath string }

// switchBufferMsg asks the App to switch to an already-open buffer by index,
// optionally scrolling to a 0-based line number (-1 = don't scroll).
// col >= 0 and matchLen > 0 trigger AtMatch; col >= 0 and matchLen == 0 trigger AtPos.
type switchBufferMsg struct {
	idx      int
	line     int
	col      int
	matchLen int
}

// jumpEntry records a single position in the edit jump list.
type jumpEntry struct {
	filePath  string
	line      int
	col       int
	undoDepth int // undo stack depth when this entry was created
}

// App is the top-level Bubble Tea model. It owns a list of editor buffers,
// routes messages to the active one, manages the file picker, and handles
// multi-buffer commands (:qa, :qa!, :wqa, ]b, [b).
// configTickMsg is sent every 2 s by the config file watcher.
type configTickMsg struct {
	newMod time.Time      // zero = file unchanged or error
	cfg    *config.Config // non-nil only when the file changed and parsed OK
}


// bufferReloadedMsg replaces a buffer model in-place after an external-change reload.
type bufferReloadedMsg struct {
	idx   int
	model client.Model
}

type App struct {
	rpc     *client.RPC
	cfg     *config.Config
	workDir string
	width   int
	height  int

	buffers []client.Model
	active  int
	status  string // app-level transient message (e.g. ":qa" error)

	picker    *filePicker // non-nil when file picker is open
	grep      *grepPicker // non-nil when workspace search picker is open
	bufPicker *bufPicker  // non-nil when buffer picker popup is open

	// fileChangedIdx is the index of a dirty buffer awaiting user decision after
	// an external modification. -1 means no prompt is active.
	// fileChangedSel is 0 for "reload" and 1 for "keep local".
	fileChangedIdx int
	fileChangedSel int

	configPath    string    // path to config.toml; empty means watch is disabled
	configModTime time.Time // mtime of last observed config file

	// Edit jump list (ctrl+o / Tab).
	jumpList []jumpEntry
	jumpIdx  int // index of last jumped-to entry; -1 = at head (not navigating)
}

// configPathAndMtime returns the config file path and its current mtime.
// Returns "" if the path cannot be determined.
func configPathAndMtime() (string, time.Time) {
	p, err := config.Path()
	if err != nil {
		return "", time.Time{}
	}
	info, err := os.Stat(p)
	if err != nil {
		return p, time.Time{}
	}
	return p, info.ModTime()
}

// watchConfig fires a configTickMsg after 2 s. The msg carries a new config
// when the file has changed, or zero values when it has not.
func watchConfig(path string, lastMod time.Time) tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		if path == "" {
			return configTickMsg{}
		}
		info, err := os.Stat(path)
		if err != nil {
			return configTickMsg{} // file removed or unreadable; keep watching
		}
		mod := info.ModTime()
		if !mod.After(lastMod) {
			return configTickMsg{} // unchanged
		}
		cfg, err := config.Load()
		if err != nil {
			return configTickMsg{newMod: mod} // parse error; update mtime to avoid hammering
		}
		return configTickMsg{newMod: mod, cfg: cfg}
	})
}

// New creates an App with a single initial buffer already open.
// startLine is 0-based; pass 0 for no jump.
func New(rpc *client.RPC, bufID uint32, content string, version uint64,
	absPath string, cfg *config.Config, fromRecovery bool,
	workDir string, startLine int) *App {

	m := client.New(rpc, bufID, content, version, absPath, cfg, fromRecovery)
	if startLine > 0 {
		m = m.AtLine(startLine)
	}
	cfgPath, cfgMod := configPathAndMtime()
	a := &App{
		rpc:            rpc,
		cfg:            cfg,
		workDir:        workDir,
		buffers:        []client.Model{m},
		fileChangedIdx: -1,
		jumpIdx:        -1,
		configPath:     cfgPath,
		configModTime:  cfgMod,
	}
	// Pre-seed terminal size so the first View() renders the editor layout
	// immediately rather than a "loading…" placeholder.
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		a.width, a.height = w, h
		a.resizeAllBuffers()
	}
	return a
}

// NewWithPicker creates an App with the file picker open immediately
// (used when indigo is started with a directory argument).
func NewWithPicker(rpc *client.RPC, cfg *config.Config, workDir string) *App {
	cfgPath, cfgMod := configPathAndMtime()
	return &App{
		rpc:            rpc,
		cfg:            cfg,
		workDir:        workDir,
		fileChangedIdx: -1,
		jumpIdx:        -1,
		configPath:     cfgPath,
		configModTime:  cfgMod,
	}
}

func (a App) Init() tea.Cmd {
	cmds := []tea.Cmd{watchConfig(a.configPath, a.configModTime)}
	if len(a.buffers) > 0 {
		cmds = append(cmds, a.buffers[0].Init())
	}
	return tea.Batch(cmds...)
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case configTickMsg:
		if msg.newMod.IsZero() {
			// File unchanged or unreadable — re-arm with the same mtime.
			return a, watchConfig(a.configPath, a.configModTime)
		}
		a.configModTime = msg.newMod
		if msg.cfg != nil {
			a.cfg = msg.cfg
			for i, m := range a.buffers {
				a.buffers[i] = m.WithConfig(msg.cfg)
			}
		}
		return a, watchConfig(a.configPath, a.configModTime)

	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		// Auto-open picker when started with a directory (no buffers yet).
		if len(a.buffers) == 0 && a.picker == nil {
			a.picker = newFilePicker(a.workDir, a.width, a.height, a.cfg.FuzzySearch)
		}
		if a.picker != nil {
			a.picker.width = msg.Width
			a.picker.height = msg.Height
		}
		if a.grep != nil {
			a.grep.width = msg.Width
			a.grep.height = msg.Height
		}
		if a.bufPicker != nil {
			a.bufPicker.width = msg.Width
			a.bufPicker.height = msg.Height
		}
		// Resize all buffers with the correct height (minus tab bar if shown).
		bufMsg := tea.WindowSizeMsg{Width: msg.Width, Height: a.bufHeight()}
		var cmds []tea.Cmd
		for i, m := range a.buffers {
			updated, cmd := m.Update(bufMsg)
			a.buffers[i] = updated.(client.Model)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)

	// ---- picker open ----
	case client.OpenPickerMsg:
		a.picker = newFilePicker(a.workDir, a.width, a.height, a.cfg.FuzzySearch)
		return a, nil

	// ---- picker result ----
	case pickedMsg:
		a.picker = nil
		return a, a.doOpenFile(msg.absPath)

	case pickerCancelledMsg:
		a.picker = nil
		if len(a.buffers) == 0 {
			return a, a.doDisconnectAndQuit()
		}
		return a, nil

	// ---- grep open ----
	case client.GrepMsg:
		if msg.Pattern == "" {
			return a, nil
		}
		a.grep = &grepPicker{
			workDir:   a.workDir,
			pattern:   msg.Pattern,
			glob:      msg.Glob,
			width:     a.width,
			height:    a.height,
			searching: true,
		}
		workDir := a.workDir
		pattern := msg.Pattern
		glob := msg.Glob
		return a, func() tea.Msg {
			results, err := searchWorkspace(workDir, pattern, glob)
			return grepResultsMsg{results: results, err: err}
		}

	case grepResultsMsg:
		if a.grep != nil {
			a.grep.searching = false
			if msg.err != nil {
				a.grep.errMsg = msg.err.Error()
			} else {
				a.grep.results = msg.results
			}
		}
		return a, nil

	case grepPickedMsg:
		a.grep = nil
		return a, a.doOpenFileAtMatch(msg.absPath, msg.line, msg.col, msg.matchLen)

	case grepCancelledMsg:
		a.grep = nil
		return a, nil

	// ---- buffer picker ----
	case client.OpenBufPickerMsg:
		if len(a.buffers) > 0 {
			a.bufPicker = newBufPicker(a.buffers, a.active, a.width, a.height)
		}
		return a, nil

	case bufPickedMsg:
		a.bufPicker = nil
		if msg.idx >= 0 && msg.idx < len(a.buffers) {
			a.active = msg.idx
		}
		return a, nil

	case bufPickerCancelledMsg:
		a.bufPicker = nil
		return a, nil

	case openFileMsg:
		return a, a.doOpenFile(msg.absPath)

	case client.OpenFileAtMsg:
		return a, a.doOpenFileAt(msg.Path, msg.Line)

	case switchBufferMsg:
		if msg.idx >= 0 && msg.idx < len(a.buffers) {
			a.active = msg.idx
			if msg.line >= 0 {
				if msg.col >= 0 && msg.matchLen > 0 {
					a.buffers[a.active] = a.buffers[a.active].AtMatch(msg.line, msg.col, msg.matchLen, a.bufHeight())
				} else if msg.col >= 0 {
					a.buffers[a.active] = a.buffers[a.active].AtPos(msg.line, msg.col)
				} else {
					a.buffers[a.active] = a.buffers[a.active].AtLine(msg.line)
				}
			}
		}
		return a, nil

	// ---- file opened ----
	case bufferOpenedMsg:
		m := msg.model
		if msg.line >= 0 {
			if msg.col >= 0 && msg.matchLen > 0 {
				m = m.AtMatch(msg.line, msg.col, msg.matchLen, a.bufHeight())
			} else if msg.col >= 0 {
				m = m.AtPos(msg.line, msg.col)
			} else {
				m = m.AtLine(msg.line)
			}
		}
		a.buffers = append(a.buffers, m)
		a.active = len(a.buffers) - 1
		// Resize ALL buffers — the tab bar may have just become visible,
		// which changes the available height for every buffer.
		resizeCmd := a.resizeAllBuffers()
		return a, tea.Batch(m.Init(), resizeCmd)

	case errorOpenMsg:
		a.status = "E: " + msg.err.Error()
		return a, nil

	// ---- buffer close ----
	case client.CloseBufferMsg:
		return a.handleCloseBuffer()

	// ---- quit all ----
	case client.QuitAllMsg:
		return a.handleQuitAll(msg)

	// ---- buffer navigation ----
	case client.NextBufferMsg:
		if len(a.buffers) > 1 {
			a.active = (a.active + 1) % len(a.buffers)
			a.status = ""
		}
		return a, nil

	case client.PrevBufferMsg:
		if len(a.buffers) > 1 {
			a.active = (a.active - 1 + len(a.buffers)) % len(a.buffers)
			a.status = ""
		}
		return a, nil

	// ---- cleanup done ----
	case appQuitMsg:
		return a, tea.Quit

	// ---- external file change notification from server ----
	case client.FileChangedMsg:
		appLog("FileChangedMsg received: BufID=%d dirty=%v numBufs=%d", msg.BufID, msg.Dirty, len(a.buffers))
		idx := -1
		for i, buf := range a.buffers {
			appLog("  buf[%d].BufID()=%d", i, buf.BufID())
			if buf.BufID() == msg.BufID {
				idx = i
				break
			}
		}
		appLog("FileChangedMsg: idx=%d", idx)
		if idx < 0 {
			return a, nil
		}
		if !msg.Dirty {
			// Buffer is clean — reload silently.
			appLog("FileChangedMsg: calling doReloadBuffer(%d)", idx)
			return a, a.doReloadBuffer(idx)
		}
		// Buffer has unsaved edits — show the overlay prompt.
		a.fileChangedIdx = idx
		return a, nil

	case bufferReloadedMsg:
		if msg.idx >= 0 && msg.idx < len(a.buffers) {
			a.buffers[msg.idx] = msg.model
			if msg.idx == a.active {
				updated, _ := a.buffers[msg.idx].Update(
					tea.WindowSizeMsg{Width: a.width, Height: a.bufHeight()})
				a.buffers[msg.idx] = updated.(client.Model)
			}
			return a, msg.model.Init()
		}
		return a, nil

	// ---- edit jump list ----
	case client.EditRecordMsg:
		a.applyEditRecord(msg)
		return a, nil

	case client.JumpBackMsg:
		return a.doJumpBack()

	case client.JumpForwardMsg:
		return a.doJumpForward()

	case client.UndoMsg:
		a.handleUndoJump(msg)
		return a, nil

	// ---- server push (plugin effects) ----
	case client.PluginShowMsgMsg:
		// Plugins are not allowed to write to the tab bar.
		return a, nil

	case client.PluginMoveCursorMsg:
		for i, buf := range a.buffers {
			if buf.BufID() == msg.BufID {
				a.buffers[i] = buf.AtLine(int(msg.Line))
				if i == a.active {
					a.active = i
				}
				break
			}
		}
		return a, nil
	}

	// Each picker intercepts key input only. Non-key messages (tickMsg, diagnosticsMsg,
	// decorationsMsg, etc.) fall through to the active buffer so the tick chain and
	// async fetch loops keep running while any picker is open.
	// File-changed overlay: intercept ALL keys while the prompt is visible.
	if a.fileChangedIdx >= 0 {
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "up", "k":
				a.fileChangedSel = 0
			case "down", "j":
				a.fileChangedSel = 1
			case "enter":
				idx := a.fileChangedIdx
				sel := a.fileChangedSel
				a.fileChangedIdx = -1
				a.fileChangedSel = 0
				if sel == 0 {
					return a, a.doReloadBuffer(idx)
				}
			case "esc":
				a.fileChangedIdx = -1
				a.fileChangedSel = 0
			}
			return a, nil // swallow all keys while overlay is open
		}
	}

	if a.picker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handlePickerKey(km)
		}
	}
	if a.grep != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handleGrepKey(km)
		}
	}
	if a.bufPicker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handleBufPickerKey(km)
		}
	}

	// No buffer open: handle essential keys at the App level.
	if len(a.buffers) == 0 {
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "ctrl+p":
				a.picker = newFilePicker(a.workDir, a.width, a.height, a.cfg.FuzzySearch)
			case "ctrl+c", "q":
				return a, a.doDisconnectAndQuit()
			}
		}
		return a, nil
	}
	updated, cmd := a.buffers[a.active].Update(msg)
	a.buffers[a.active] = updated.(client.Model)
	return a, cmd
}

// handlePickerKey routes key events to the file picker.
func (a App) handlePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return pickerCancelledMsg{} }
	case "enter":
		path := a.picker.selected()
		if path == "" {
			// No filtered match — treat the query as a direct (possibly relative) path.
			q := strings.TrimSpace(a.picker.query)
			if q == "" {
				return a, nil
			}
			if !filepath.IsAbs(q) {
				q = filepath.Join(a.workDir, q)
			}
			return a, func() tea.Msg { return pickedMsg{absPath: q} }
		}
		return a, func() tea.Msg { return pickedMsg{absPath: path} }
	case "up", "ctrl+p":
		a.picker.moveUp()
	case "down", "ctrl+n":
		a.picker.moveDown()
	case "backspace":
		q := []rune(a.picker.query)
		if len(q) > 0 {
			a.picker.setQuery(string(q[:len(q)-1]))
		}
	default:
		if len(msg.Runes) > 0 {
			a.picker.setQuery(a.picker.query + string(msg.Runes))
		}
	}
	return a, nil
}

// handleBufPickerKey routes key events to the buffer picker popup.
func (a App) handleBufPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return bufPickerCancelledMsg{} }
	case "enter":
		idx := a.bufPicker.selected()
		return a, func() tea.Msg { return bufPickedMsg{idx: idx} }
	case "up", "k":
		a.bufPicker.moveUp()
	case "down", "j":
		a.bufPicker.moveDown()
	}
	return a, nil
}

// handleGrepKey routes key events to the workspace search picker.
func (a App) handleGrepKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		return a, func() tea.Msg { return grepCancelledMsg{} }
	case "enter":
		if a.grep != nil && len(a.grep.results) > 0 {
			r := a.grep.results[a.grep.cursor]
			absPath := filepath.Join(a.grep.workDir, r.RelPath)
			return a, func() tea.Msg {
				return grepPickedMsg{absPath: absPath, line: r.Line, col: r.Col, matchLen: r.MatchLen}
			}
		}
	case "up", "ctrl+p", "k":
		if a.grep != nil {
			a.grep.moveUp()
		}
	case "down", "ctrl+n", "j":
		if a.grep != nil {
			a.grep.moveDown()
		}
	}
	return a, nil
}

// resizeAllBuffers sends the current effective height to every buffer.
// Must be called whenever the tab bar appears or disappears (buffer count
// crosses the 1↔2 boundary) so no buffer is off by one row.
func (a *App) resizeAllBuffers() tea.Cmd {
	if a.width == 0 {
		return nil
	}
	bh := a.bufHeight()
	var cmds []tea.Cmd
	for i := range a.buffers {
		updated, cmd := a.buffers[i].Update(tea.WindowSizeMsg{Width: a.width, Height: bh})
		a.buffers[i] = updated.(client.Model)
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

// handleCloseBuffer closes the active buffer and switches to the next, or quits.
func (a App) handleCloseBuffer() (tea.Model, tea.Cmd) {
	a.buffers = append(a.buffers[:a.active], a.buffers[a.active+1:]...)
	if len(a.buffers) == 0 {
		return a, a.doDisconnectAndQuit()
	}
	if a.active >= len(a.buffers) {
		a.active = len(a.buffers) - 1
	}
	// Resize remaining buffers — tab bar may have just disappeared.
	return a, a.resizeAllBuffers()
}

// handleQuitAll implements :qa, :qa!, :wqa.
func (a App) handleQuitAll(msg client.QuitAllMsg) (tea.Model, tea.Cmd) {
	if msg.SaveAll {
		return a, a.doSaveAllAndQuit()
	}
	if !msg.Force {
		dirty := 0
		for _, m := range a.buffers {
			if m.Dirty() {
				dirty++
			}
		}
		if dirty > 0 {
			a.status = fmt.Sprintf("E: %d unsaved file(s) (use :qa! to force)", dirty)
			return a, nil
		}
	}
	return a, a.doCloseAllAndQuit()
}

// bufHeight returns the height available to buffer views (minus tab bar).
func (a App) bufHeight() int {
	if a.showTabBar() {
		return max(1, a.height-1)
	}
	return a.height
}

func (a App) showTabBar() bool {
	return !a.cfg.HideTabs && len(a.buffers) > 1
}

// ---- async commands ----

type bufferOpenedMsg struct {
	model    client.Model
	line     int // 0-based target line; -1 = no jump
	col      int // -1 = use AtLine; >= 0 with matchLen > 0 = AtMatch; >= 0 with matchLen == 0 = AtPos
	matchLen int
}
type errorOpenMsg struct{ err error }

func (a App) doOpenFile(absPath string) tea.Cmd {
	return a.doOpenFileAt(absPath, -1)
}

func (a App) doOpenFileAt(absPath string, line int) tea.Cmd {
	// Check if already open — switch to it instead of opening again.
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			idx := i
			return func() tea.Msg { return switchBufferMsg{idx: idx, line: line, col: -1} }
		}
	}
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: -1}
	}
}

func (a App) doOpenFileAtMatch(absPath string, line, col, matchLen int) tea.Cmd {
	// Check if already open — switch to it instead of opening again.
	for i, m := range a.buffers {
		if m.FilePath() == absPath {
			idx := i
			return func() tea.Msg {
				return switchBufferMsg{idx: idx, line: line, col: col, matchLen: matchLen}
			}
		}
	}
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: col, matchLen: matchLen}
	}
}

// applyEditRecord adjusts existing jump entries that were shifted by the edit
// described in msg, then adds a new entry for msg.{Line,Col}.
func (a *App) applyEditRecord(msg client.EditRecordMsg) {
	if msg.FilePath == "" {
		return
	}
	if msg.LineDelta != 0 {
		atLine := msg.AtLine
		delta := msg.LineDelta
		n := 0
		for _, e := range a.jumpList {
			if e.filePath != msg.FilePath {
				a.jumpList[n] = e
				n++
				continue
			}
			if delta < 0 {
				// Lines [atLine, atLine-delta) were deleted.
				deletedTo := atLine - delta
				if e.line >= deletedTo {
					e.line += delta // shift back
					a.jumpList[n] = e
					n++
				} else if e.line >= atLine {
					// Entry was inside the deleted range — drop it.
				} else {
					a.jumpList[n] = e
					n++
				}
			} else {
				// Lines were inserted after atLine.
				if e.line > atLine {
					e.line += delta
				}
				a.jumpList[n] = e
				n++
			}
		}
		a.jumpList = a.jumpList[:n]
		if a.jumpIdx >= n {
			a.jumpIdx = n - 1
		}
	}
	a.recordEdit(msg.FilePath, msg.Line, msg.Col, msg.UndoDepth)
}

// recordEdit appends {filePath, line, col, undoDepth} to the jump list.
// A new edit invalidates forward history; consecutive edits on the same
// file+line collapse into one entry (updating col and undoDepth in-place).
func (a *App) recordEdit(filePath string, line, col, undoDepth int) {
	if filePath == "" {
		return
	}
	if a.jumpIdx >= 0 {
		// Truncate any forward entries — a new edit rewrites the future.
		a.jumpList = a.jumpList[:a.jumpIdx+1]
		a.jumpIdx = -1
	}
	if n := len(a.jumpList); n > 0 {
		last := a.jumpList[n-1]
		if last.filePath == filePath && last.line == line {
			a.jumpList[n-1].col = col
			a.jumpList[n-1].undoDepth = undoDepth
			return
		}
	}
	a.jumpList = append(a.jumpList, jumpEntry{filePath: filePath, line: line, col: col, undoDepth: undoDepth})
	if len(a.jumpList) > 100 {
		a.jumpList = a.jumpList[len(a.jumpList)-100:]
	}
}

// handleUndoJump reverses the line-shift caused by the undone operation and
// removes jump entries that no longer exist (their undoDepth > newDepth).
func (a *App) handleUndoJump(msg client.UndoMsg) {
	// Step 1: re-adjust line numbers for entries that were shifted when the
	// undone edit was originally recorded.
	if msg.LineDelta != 0 {
		atLine := msg.AtLine
		delta := msg.LineDelta
		n := 0
		for _, e := range a.jumpList {
			if e.filePath != msg.FilePath {
				a.jumpList[n] = e
				n++
				continue
			}
			if delta < 0 {
				// Undo of a forward insert: delete the inserted lines.
				deletedTo := atLine - delta
				if e.line >= deletedTo {
					e.line += delta
					a.jumpList[n] = e
					n++
				} else if e.line >= atLine {
					// inside deleted range — drop
				} else {
					a.jumpList[n] = e
					n++
				}
			} else {
				// Undo of a forward delete: re-insert the deleted lines.
				// Entries at >= atLine were shifted here from higher lines; use
				// >= (not >) so entries that landed exactly at atLine are restored.
				if e.line >= atLine {
					e.line += delta
				}
				a.jumpList[n] = e
				n++
			}
		}
		a.jumpList = a.jumpList[:n]
	}
	// Step 2: prune entries created by edits that are now undone.
	n := 0
	for _, e := range a.jumpList {
		if e.filePath == msg.FilePath && e.undoDepth > msg.NewDepth {
			continue
		}
		a.jumpList[n] = e
		n++
	}
	a.jumpList = a.jumpList[:n]
	// Reset navigation state: the cursor is now at the undo-restored position,
	// not at any known jump entry. Clamping jumpIdx to n-1 when entries were
	// removed causes doJumpBack to believe we're already at the oldest entry
	// (jumpIdx==0) and silently do nothing. Resetting to -1 lets the next
	// jump-back start fresh from the end of the surviving list.
	a.jumpIdx = -1
}

func (a App) doJumpBack() (tea.Model, tea.Cmd) {
	if len(a.jumpList) == 0 {
		return a, nil
	}
	if a.jumpIdx == -1 {
		a.jumpIdx = len(a.jumpList) - 1
	} else if a.jumpIdx > 0 {
		a.jumpIdx--
	} else {
		return a, nil // already at the oldest entry
	}
	return a.jumpToEntry(a.jumpList[a.jumpIdx])
}

func (a App) doJumpForward() (tea.Model, tea.Cmd) {
	if a.jumpIdx < 0 || a.jumpIdx >= len(a.jumpList)-1 {
		return a, nil
	}
	a.jumpIdx++
	return a.jumpToEntry(a.jumpList[a.jumpIdx])
}

// jumpToEntry switches to (or opens) the buffer for e and positions the cursor.
func (a App) jumpToEntry(e jumpEntry) (tea.Model, tea.Cmd) {
	for i, m := range a.buffers {
		if m.FilePath() == e.filePath {
			a.active = i
			a.buffers[i] = m.AtPos(e.line, e.col)
			return a, nil
		}
	}
	return a, a.doOpenFileAtPos(e.filePath, e.line, e.col)
}

// doOpenFileAtPos opens absPath in a new buffer positioned at (line, col).
func (a App) doOpenFileAtPos(absPath string, line, col int) tea.Cmd {
	rpc := a.rpc
	cfg := a.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absPath)
		if err != nil {
			return errorOpenMsg{err}
		}
		m := client.New(rpc, bufID, content, version, absPath, cfg, fromRecovery)
		return bufferOpenedMsg{model: m, line: line, col: col}
	}
}

// doReloadBuffer closes the server-side buffer and reopens it so the server
// re-reads the file from disk. The result replaces the buffer at idx in-place.
func (a App) doReloadBuffer(idx int) tea.Cmd {
	if idx < 0 || idx >= len(a.buffers) {
		appLog("doReloadBuffer: idx %d out of range (len=%d)", idx, len(a.buffers))
		return nil
	}
	m := a.buffers[idx]
	path := m.FilePath()
	oldBufID := m.BufID()
	rpc := a.rpc
	cfg := a.cfg
	appLog("doReloadBuffer: queuing cmd for idx=%d path=%q oldBufID=%d", idx, path, oldBufID)
	return func() tea.Msg {
		appLog("doReloadBuffer: cmd running, calling CloseBuffer bufID=%d", oldBufID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rpc.CloseBuffer(ctx, oldBufID); err != nil {
			appLog("doReloadBuffer: CloseBuffer error: %v", err)
		}
		appLog("doReloadBuffer: CloseBuffer done, calling OpenFile path=%q", path)
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, path)
		if err != nil {
			appLog("doReloadBuffer: OpenFile error: %v", err)
			return errorOpenMsg{err}
		}
		appLog("doReloadBuffer: OpenFile done, new bufID=%d contentLen=%d", bufID, len(content))
		newModel := client.New(rpc, bufID, content, version, path, cfg, fromRecovery)
		return bufferReloadedMsg{idx: idx, model: newModel}
	}
}

func (a App) doCloseAllAndQuit() tea.Cmd {
	buffers := a.buffers
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, m := range buffers {
			rpc.CloseBuffer(ctx, m.BufID()) //nolint:errcheck
		}
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}

func (a App) doSaveAllAndQuit() tea.Cmd {
	buffers := a.buffers
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, m := range buffers {
			if m.Dirty() {
				rpc.Save(ctx, m.BufID()) //nolint:errcheck
			}
			rpc.CloseBuffer(ctx, m.BufID()) //nolint:errcheck
		}
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}

func (a App) doDisconnectAndQuit() tea.Cmd {
	rpc := a.rpc
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		rpc.Disconnect(ctx) //nolint:errcheck
		return appQuitMsg{}
	}
}

// ---- View ----

var (
	tabBarBg      = lipgloss.Color("#065A96")
	tabActiveStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("#087AC8")).
			Foreground(lipgloss.Color("#FFFFFF")).
			Bold(true)
	tabInactiveStyle = lipgloss.NewStyle().
				Background(tabBarBg).
				Foreground(lipgloss.Color("#AABBCC"))
	tabDirtyMark = "● "
	tabBarFill   = lipgloss.NewStyle().Background(tabBarBg)
)

func (a App) View() string {
	if a.picker != nil {
		return a.picker.View()
	}
	if a.grep != nil {
		return a.grep.View()
	}
	if len(a.buffers) == 0 {
		return "No buffer open. Press ctrl+p to open a file."
	}

	var sb strings.Builder
	if a.showTabBar() {
		sb.WriteString(a.renderTabBar())
		sb.WriteByte('\n')
	}
	sb.WriteString(a.buffers[a.active].View())
	base := sb.String()

	if a.fileChangedIdx >= 0 {
		return overlayCenter(base, renderFileChangedPrompt(a.width, a.fileChangedSel), a.width, a.height)
	}
	if a.bufPicker != nil {
		return overlayCenter(base, a.bufPicker.render(), a.width, a.height)
	}
	return base
}

func renderFileChangedPrompt(w, sel int) string {
	innerW := 40
	if w > 0 && w*2/3 < innerW {
		innerW = w * 2 / 3
	}
	if innerW < 30 {
		innerW = 30
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FFAA44")).
		Background(lipgloss.Color("#1E2A38"))
	titleStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#0D1B2A")).
		Foreground(lipgloss.Color("#FFAA44")).
		Bold(true).
		Padding(0, 1)
	divStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#FFAA44"))
	itemStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#1E2A38")).
		Foreground(lipgloss.Color("#AABBCC")).
		Padding(0, 1)
	selStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#2D5F8A")).
		Foreground(lipgloss.Color("#FFFFFF")).
		Padding(0, 1)

	style := func(idx int, label string) string {
		if idx == sel {
			return selStyle.Width(innerW).Render(label)
		}
		return itemStyle.Width(innerW).Render(label)
	}

	rows := []string{
		titleStyle.Width(innerW).Render("⚠  File changed on disk"),
		divStyle.Render(strings.Repeat("─", innerW)),
		style(0, "  Reload  (discard local changes)"),
		style(1, "  Keep local version"),
	}
	return borderStyle.Render(strings.Join(rows, "\n"))
}

// overlayCenter splices popup (a multi-line box) into the centre of base,
// using ANSI-aware left/right truncation so editor colours show on both sides.
func overlayCenter(base, popup string, termW, termH int) string {
	baseLines := strings.Split(base, "\n")
	popLines := strings.Split(popup, "\n")

	popH := len(popLines)
	popW := lipgloss.Width(popLines[0])

	startRow := (termH - popH) / 2
	startCol := (termW - popW) / 2
	if startCol < 0 {
		startCol = 0
	}

	out := make([]string, termH)
	for i := range out {
		if i < len(baseLines) {
			out[i] = baseLines[i]
		} else {
			out[i] = strings.Repeat(" ", termW)
		}
	}

	for pi, popLine := range popLines {
		ri := startRow + pi
		if ri < 0 || ri >= termH {
			continue
		}
		baseLine := out[ri]
		left := ansi.Truncate(baseLine, startCol, "")
		// Pad left side if baseLine is shorter than startCol (e.g. empty lines).
		leftW := lipgloss.Width(left)
		if leftW < startCol {
			left += strings.Repeat(" ", startCol-leftW)
		}
		right := ansi.TruncateLeft(baseLine, startCol+popW, "")
		out[ri] = left + popLine + right
	}

	return strings.Join(out, "\n")
}

func (a App) renderTabBar() string {
	var sb strings.Builder
	used := 0
	for i, m := range a.buffers {
		name := filepath.Base(m.FilePath())
		dirty := ""
		if m.Dirty() {
			dirty = tabDirtyMark
		}
		label := fmt.Sprintf("  %s%s  ", dirty, name)
		var rendered string
		if i == a.active {
			rendered = tabActiveStyle.Render(label)
		} else {
			rendered = tabInactiveStyle.Render(label)
		}
		sb.WriteString(rendered)
		used += lipgloss.Width(rendered)
	}
	// Show app-level status at the right if set.
	if a.status != "" {
		gap := a.width - used - lipgloss.Width(a.status)
		if gap > 0 {
			sb.WriteString(tabBarFill.Render(strings.Repeat(" ", gap)))
		}
		sb.WriteString(tabInactiveStyle.Foreground(lipgloss.Color("#FF5555")).Render(a.status))
	} else if used < a.width {
		sb.WriteString(tabBarFill.Render(strings.Repeat(" ", a.width-used)))
	}
	return sb.String()
}
