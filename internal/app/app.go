package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
)

// appQuitMsg is returned by the cleanup cmd to trigger program exit.
type appQuitMsg struct{}

// openFileMsg asks the App to open a file by absolute path.
type openFileMsg struct{ absPath string }

// switchBufferMsg asks the App to switch to an already-open buffer by index,
// optionally scrolling to a 0-based line number (-1 = don't scroll).
type switchBufferMsg struct {
	idx  int
	line int
}

// App is the top-level Bubble Tea model. It owns a list of editor buffers,
// routes messages to the active one, manages the file picker, and handles
// multi-buffer commands (:qa, :qa!, :wqa, ]b, [b).
type App struct {
	rpc     *client.RPC
	cfg     *config.Config
	workDir string
	width   int
	height  int

	buffers []client.Model
	active  int
	status  string // app-level transient message (e.g. ":qa" error)

	picker *filePicker // non-nil when file picker is open
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
	return &App{
		rpc:     rpc,
		cfg:     cfg,
		workDir: workDir,
		buffers: []client.Model{m},
	}
}

// NewWithPicker creates an App with the file picker open immediately
// (used when indigo is started with a directory argument).
func NewWithPicker(rpc *client.RPC, cfg *config.Config, workDir string) *App {
	return &App{
		rpc:     rpc,
		cfg:     cfg,
		workDir: workDir,
	}
}

func (a App) Init() tea.Cmd {
	if len(a.buffers) == 0 {
		// Picker-only start: no buffer yet.
		return nil
	}
	return a.buffers[0].Init()
}

func (a App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

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
		// Resize all buffers with the correct height (minus tab bar if shown).
		bufMsg := tea.WindowSizeMsg{Width: msg.Width, Height: a.bufHeight()}
		for i, m := range a.buffers {
			updated, _ := m.Update(bufMsg)
			a.buffers[i] = updated.(client.Model)
		}
		return a, nil

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

	case openFileMsg:
		return a, a.doOpenFile(msg.absPath)

	case client.OpenFileAtMsg:
		return a, a.doOpenFileAt(msg.Path, msg.Line)

	case switchBufferMsg:
		if msg.idx >= 0 && msg.idx < len(a.buffers) {
			a.active = msg.idx
			if msg.line >= 0 {
				a.buffers[a.active] = a.buffers[a.active].AtLine(msg.line)
			}
		}
		return a, nil

	// ---- file opened ----
	case bufferOpenedMsg:
		m := msg.model
		if msg.line >= 0 {
			m = m.AtLine(msg.line)
		}
		a.buffers = append(a.buffers, m)
		a.active = len(a.buffers) - 1
		// Resize ALL buffers — the tab bar may have just become visible,
		// which changes the available height for every buffer.
		a.resizeAllBuffers()
		return a, m.Init()

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

	// ---- server push (plugin effects) ----
	case client.PluginShowMsgMsg:
		a.status = msg.Text
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

	// Picker intercepts all key input when open.
	if a.picker != nil {
		if km, ok := msg.(tea.KeyMsg); ok {
			return a.handlePickerKey(km)
		}
		return a, nil
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

// resizeAllBuffers sends the current effective height to every buffer.
// Must be called whenever the tab bar appears or disappears (buffer count
// crosses the 1↔2 boundary) so no buffer is off by one row.
func (a *App) resizeAllBuffers() {
	if a.width == 0 {
		return
	}
	bh := a.bufHeight()
	for i := range a.buffers {
		updated, _ := a.buffers[i].Update(tea.WindowSizeMsg{Width: a.width, Height: bh})
		a.buffers[i] = updated.(client.Model)
	}
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
	a.resizeAllBuffers()
	return a, nil
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
	model client.Model
	line  int // 0-based target line; -1 = no jump
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
			return func() tea.Msg { return switchBufferMsg{idx: idx, line: line} }
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
		return bufferOpenedMsg{model: m, line: line}
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
	if len(a.buffers) == 0 {
		return "No buffer open. Press ctrl+p to open a file."
	}

	var sb strings.Builder
	if a.showTabBar() {
		sb.WriteString(a.renderTabBar())
		sb.WriteByte('\n')
	}
	sb.WriteString(a.buffers[a.active].View())
	return sb.String()
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
