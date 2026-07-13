package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	filePath         string
	line             int
	col              int
	undoDepth        int  // undo stack depth when this entry was created
	active           bool // false when the entry's line has been deleted
	deactivatedDepth int  // undoDepth of the delete that deactivated this entry
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

	symbolPicker    *symbolPickerState    // non-nil when workspace symbol picker is open
	docSymbolPicker *docSymbolPickerState // non-nil when document symbol picker is open
	refPicker       *refPickerState       // non-nil when reference picker is open

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

	// Plugin-driven UI overlays.
	pluginPopup *appPluginPopup // non-nil when a plugin popup is visible
	pluginInput *appPluginInput // non-nil when a plugin input prompt is visible
}

// appPluginPopup holds state for a plugin-driven interactive list overlay.
type appPluginPopup struct {
	title  string
	items  []client.ClientPopupItem
	idx    int
	width  int
	height int
}

// appPluginInput holds state for a plugin-driven text-input overlay.
type appPluginInput struct {
	title       string
	placeholder string
	text        string
	width       int
	height      int
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

	m := client.New(rpc, bufID, content, version, absPath, workDir, cfg, fromRecovery)
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
		if a.pluginPopup != nil {
			a.pluginPopup.width = msg.Width
			a.pluginPopup.height = msg.Height
		}
		if a.pluginInput != nil {
			a.pluginInput.width = msg.Width
			a.pluginInput.height = msg.Height
		}
		if a.symbolPicker != nil {
			a.symbolPicker.width = msg.Width
			a.symbolPicker.height = msg.Height
		}
		if a.docSymbolPicker != nil {
			a.docSymbolPicker.width = msg.Width
			a.docSymbolPicker.height = msg.Height
		}
		if a.refPicker != nil {
			a.refPicker.width = msg.Width
			a.refPicker.height = msg.Height
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
		if msg.Col >= 0 {
			return a, a.doOpenFileAtPos(msg.Path, msg.Line, msg.Col)
		}
		return a, a.doOpenFileAt(msg.Path, msg.Line)

	case switchBufferMsg:
		if msg.idx >= 0 && msg.idx < len(a.buffers) {
			a.active = msg.idx
			if msg.line >= 0 {
				if msg.col >= 0 && msg.matchLen > 0 {
					a.buffers[a.active] = a.buffers[a.active].AtMatch(msg.line, msg.col, msg.matchLen, a.bufHeight())
				} else if msg.col >= 0 {
					a.buffers[a.active] = a.buffers[a.active].AtPos(msg.line, msg.col, a.bufHeight())
				} else {
					a.buffers[a.active] = a.buffers[a.active].AtLine(msg.line)
				}
			}
		}
		return a, a.buffers[a.active].ReportActiveContextCmd()

	// ---- file opened ----
	case bufferOpenedMsg:
		m := msg.model
		if msg.line >= 0 {
			if msg.col >= 0 && msg.matchLen > 0 {
				m = m.AtMatch(msg.line, msg.col, msg.matchLen, a.bufHeight())
			} else if msg.col >= 0 {
				m = m.AtPos(msg.line, msg.col, a.bufHeight())
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
		return a, a.buffers[a.active].ReportActiveContextCmd()

	case client.PrevBufferMsg:
		if len(a.buffers) > 1 {
			a.active = (a.active - 1 + len(a.buffers)) % len(a.buffers)
			a.status = ""
		}
		return a, a.buffers[a.active].ReportActiveContextCmd()

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

	// ---- plugin-driven UI ----
	case client.ShowPluginPopupMsg:
		a.pluginPopup = &appPluginPopup{
			title:  msg.Title,
			items:  msg.Items,
			idx:    0,
			width:  a.width,
			height: a.height,
		}
		return a, nil

	case client.HidePluginPopupMsg:
		a.pluginPopup = nil
		return a, nil

	case client.ShowInputPromptMsg:
		a.pluginInput = &appPluginInput{
			title:       msg.Title,
			placeholder: msg.Placeholder,
			width:       a.width,
			height:      a.height,
		}
		return a, nil

	case client.HideInputPromptMsg:
		a.pluginInput = nil
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

	case client.OpenSymbolPickerMsg:
		if len(a.buffers) > 0 {
			a.symbolPicker = newSymbolPicker(msg.BufID, a.width, a.height)
		}
		return a, nil

	case client.OpenDocSymbolPickerMsg:
		a.docSymbolPicker = newDocSymbolPicker(msg.Syms, a.width, a.height)
		return a, nil

	case client.OpenRefPickerMsg:
		if len(msg.Refs) > 0 {
			a.refPicker = newRefPicker(msg.Title, msg.Refs, a.width, a.height)
		}
		return a, nil

	case symbolResultsMsg:
		if a.symbolPicker != nil && msg.query == a.symbolPicker.query {
			a.symbolPicker.results = msg.syms
			a.symbolPicker.cursor = 0
			a.symbolPicker.loading = false
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
	if a.pluginInput != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handlePluginInputKey(km)
		}
	}
	if a.pluginPopup != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handlePluginPopupKey(km)
		}
	}
	if a.symbolPicker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handleSymbolPickerKey(km)
		}
	}
	if a.docSymbolPicker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handleDocSymbolPickerKey(km)
		}
	}
	if a.refPicker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handleRefPickerKey(km)
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
