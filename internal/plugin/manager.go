package plugin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	capnp "capnproto.org/go/capnp/v3"
	"capnproto.org/go/capnp/v3/rpc"

	"github.com/BurntSushi/toml"
	"github.com/indiejames/indigo/internal/proto/pluginproto"
)

// ServerBridge is implemented by the server and injected into each EditorApi.
// Plugins call EditorApi methods which delegate here for buffer access and effects.
type ServerBridge interface {
	// Document queries
	PluginReadBuffer(bufID uint32) (string, error)
	PluginReadLines(bufID uint32, start, end uint32) ([]string, error)
	PluginReadRange(bufID uint32, fromLine, fromCol, toLine, toCol uint32) (string, error)
	PluginWordAt(bufID uint32, line, col uint32) (startLine, startCol, endLine, endCol uint32, found bool, err error)
	PluginBufferInfo(bufID uint32) (path, langID string, lineCount uint32, isDirty bool, err error)
	PluginVisibleRange(clientID uint64) (startLine, endLine uint32, err error)

	// Editor effects
	PluginApplyEdit(bufID uint32, edits []TextEdit) error
	PluginMoveCursor(bufID uint32, line, col uint32) error
	PluginOpenFile(path string, line uint32) error
	PluginShowMessage(text string)
	PluginRunProcess(cmd string, args []string) (stdout, stderr string, exitCode int32, err error)

	// Key registration notification — called so the server can push to clients.
	PluginKeyRegistered(trigger string)
}

// PluginDecorationKind mirrors the capnp enum for use in plain-Go types.
type PluginDecorationKind int

const (
	DecorationKindGutter    PluginDecorationKind = 0
	DecorationKindOverlay   PluginDecorationKind = 1
	DecorationKindStatusBar PluginDecorationKind = 2
)

// PluginDecoration is a single decoration returned by a plugin's DecorationProvider.
type PluginDecoration struct {
	Line uint32
	Col  uint32
	Text string
	Kind PluginDecorationKind
}

// TextEdit is a plain-Go representation of a capnp TextEdit, used in ServerBridge.
type TextEdit struct {
	FromLine, FromCol uint32
	ToLine, ToCol     uint32
	NewText           string
}

// PluginToml is the plugin manifest format.
type PluginToml struct {
	Name        string            `toml:"name"`
	Version     string            `toml:"version"`
	Description string            `toml:"description"`
	Binaries    map[string]string `toml:"binaries"`
}

// registeredPlugin holds a running plugin's process, RPC connection, and
// the handlers it registered during initialize.
type registeredPlugin struct {
	name    string
	process *os.Process
	rpcConn *rpc.Conn

	mu            sync.RWMutex
	keyBindings   map[string]pluginproto.KeyHandler
	insertHooks   map[string]pluginproto.KeyHandler
	commands      map[string]pluginproto.CommandHandler
	bufHandler    pluginproto.BufferEventHandler
	decorProvider pluginproto.DecorationProvider
}

func (p *registeredPlugin) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range p.keyBindings {
		h.Release()
	}
	for _, h := range p.insertHooks {
		h.Release()
	}
	for _, h := range p.commands {
		h.Release()
	}
	p.bufHandler.Release()
	p.decorProvider.Release()
}

// Manager discovers plugins in the user's plugins directory and manages their
// processes for the lifetime of one workspace session.
type Manager struct {
	mu       sync.Mutex
	plugins  []*registeredPlugin
	workDir  string
	bridge   ServerBridge
}

// NewManager creates a Manager for the given workspace root.
// bridge provides access to server state; it may be nil if no bridge is
// available yet (plugin effects will return errors).
func NewManager(workDir string, bridge ServerBridge) *Manager {
	return &Manager{
		workDir: workDir,
		bridge:  bridge,
	}
}

// Start discovers all installed plugins and starts them. Plugins that fail to
// start are skipped; the error is logged but does not propagate.
func (m *Manager) Start(ctx context.Context) error {
	dir, err := pluginsConfigDir()
	if err != nil {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read plugins dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginDir := filepath.Join(dir, entry.Name())
		manifest, err := loadManifest(pluginDir)
		if err != nil {
			continue
		}
		binaryPath, err := selectBinary(pluginDir, manifest)
		if err != nil {
			continue
		}
		if err := m.startPlugin(ctx, manifest.Name, binaryPath); err != nil {
			// One bad plugin must not prevent others from loading.
			continue
		}
	}
	return nil
}

func (m *Manager) startPlugin(ctx context.Context, name, binaryPath string) error {
	sockPath := m.pluginSocketPath(name)
	os.Remove(sockPath) //nolint:errcheck

	proc, err := os.StartProcess(binaryPath,
		[]string{binaryPath, "--socket", sockPath},
		&os.ProcAttr{Files: []*os.File{nil, nil, nil}},
	)
	if err != nil {
		return fmt.Errorf("start plugin %s: %w", name, err)
	}

	if err := waitForSocket(sockPath, 5*time.Second); err != nil {
		proc.Kill() //nolint:errcheck
		return fmt.Errorf("plugin %s socket: %w", name, err)
	}

	netConn, err := net.Dial("unix", sockPath)
	if err != nil {
		proc.Kill() //nolint:errcheck
		return fmt.Errorf("plugin %s connect: %w", name, err)
	}

	reg := &registeredPlugin{
		name:        name,
		process:     proc,
		keyBindings: make(map[string]pluginproto.KeyHandler),
		insertHooks: make(map[string]pluginproto.KeyHandler),
		commands:    make(map[string]pluginproto.CommandHandler),
	}

	apiServer := &editorApiServer{reg: reg, bridge: m.bridge}
	api := pluginproto.EditorApi_ServerToClient(apiServer)

	rpcConn := rpc.NewConn(rpc.NewStreamTransport(netConn), &rpc.Options{
		BootstrapClient: capnp.Client(api),
	})
	reg.rpcConn = rpcConn

	plugin := pluginproto.Plugin(rpcConn.Bootstrap(ctx))
	defer plugin.Release()

	fut, rel := plugin.Initialize(ctx, func(p pluginproto.Plugin_initialize_Params) error {
		return p.SetApi(pluginproto.EditorApi(capnp.Client(api).AddRef()))
	})
	defer rel()

	if _, err := fut.Struct(); err != nil {
		rpcConn.Close()
		proc.Kill() //nolint:errcheck
		return fmt.Errorf("plugin %s initialize: %w", name, err)
	}

	m.mu.Lock()
	m.plugins = append(m.plugins, reg)
	m.mu.Unlock()
	return nil
}

// GetDecorations calls every registered DecorationProvider and aggregates the results.
// startLine and endLine are the inclusive visible range (0-based buffer line numbers).
func (m *Manager) GetDecorations(ctx context.Context, bufID, startLine, endLine uint32) []PluginDecoration {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()

	var all []PluginDecoration
	for _, p := range plugins {
		p.mu.RLock()
		provider := p.decorProvider
		p.mu.RUnlock()
		if !provider.IsValid() {
			continue
		}

		tctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		fut, rel := provider.GetDecorations(tctx, func(ps pluginproto.DecorationProvider_getDecorations_Params) error {
			ps.SetBufId(bufID)
			rng, err := ps.NewVisibleRange()
			if err != nil {
				cancel()
				return err
			}
			start, err := rng.NewStart()
			if err != nil {
				cancel()
				return err
			}
			start.SetLine(startLine)
			end, err := rng.NewEnd()
			if err != nil {
				cancel()
				return err
			}
			end.SetLine(endLine)
			return nil
		})
		res, err := fut.Struct()
		rel()
		cancel()
		if err != nil {
			continue
		}
		rawList, err := res.Decorations()
		if err != nil {
			continue
		}
		for i := range rawList.Len() {
			item := rawList.At(i)
			text, err := item.Text()
			if err != nil {
				continue
			}
			all = append(all, PluginDecoration{
				Line: item.Line(),
				Col:  item.Col(),
				Text: text,
				Kind: PluginDecorationKind(item.Kind()),
			})
		}
	}
	return all
}

// Shutdown cleanly stops all plugin processes.
// Plugins are given a 2-second grace period, then SIGKILL.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	plugins := m.plugins
	m.plugins = nil
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, p := range plugins {
			p.rpcConn.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	for _, p := range plugins {
		p.process.Kill() //nolint:errcheck
		p.release()
	}
}

// HandleKey routes a keypress to the first plugin that registered it.
// Returns handled=false immediately if no plugin owns the key.
// Plugin handlers are called with a 30 ms deadline; timeout → handled=false.
func (m *Manager) HandleKey(ctx context.Context, key, mode string) (
	handled bool, edits []TextEdit, cursorLine, cursorCol uint32, hasCursor bool, captureKeys uint32, err error,
) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()

	for _, p := range plugins {
		p.mu.RLock()
		handler, ok := p.keyBindings[key]
		p.mu.RUnlock()
		if !ok {
			continue
		}

		tctx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
		defer cancel()

		fut, rel := handler.Handle(tctx, func(ps pluginproto.KeyHandler_handle_Params) error {
			ps.SetKey(key) //nolint:errcheck
			kctx, err := ps.NewCtx()
			if err != nil {
				return err
			}
			return kctx.SetMode(mode)
		})
		defer rel()

		res, callErr := fut.Struct()
		if callErr != nil {
			// Timeout or error: fall through as if not handled.
			return false, nil, 0, 0, false, 0, nil
		}
		resp, respErr := res.Response()
		if respErr != nil {
			return false, nil, 0, 0, false, 0, respErr
		}
		if !resp.Handled() {
			return false, nil, 0, 0, false, 0, nil
		}

		// Collect edits from the response.
		rawEdits, _ := resp.Edits()
		edits = make([]TextEdit, rawEdits.Len())
		for i := range edits {
			item := rawEdits.At(i)
			from, _ := item.From()
			to, _ := item.To()
			newText, _ := item.NewText()
			edits[i] = TextEdit{
				FromLine: from.Line(), FromCol: from.Col(),
				ToLine:   to.Line(), ToCol: to.Col(),
				NewText:  newText,
			}
		}

		hasCursor = resp.HasCursor()
		if hasCursor {
			pos, _ := resp.CursorPos()
			cursorLine = pos.Line()
			cursorCol = pos.Col()
		}
		captureKeys = resp.CaptureKeys()
		return true, edits, cursorLine, cursorCol, hasCursor, captureKeys, nil
	}
	return false, nil, 0, 0, false, 0, nil
}

// DispatchBufferOpen fires onOpen for all plugins that registered a buffer handler.
// Called asynchronously — never blocks the server render loop.
func (m *Manager) DispatchBufferOpen(ctx context.Context, bufID uint32, path string) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		p.mu.RLock()
		h := p.bufHandler
		p.mu.RUnlock()
		if !h.IsValid() {
			continue
		}
		go func(h pluginproto.BufferEventHandler) {
			fut, rel := h.OnOpen(ctx, func(ps pluginproto.BufferEventHandler_onOpen_Params) error {
				ev, err := ps.NewEvent()
				if err != nil {
					return err
				}
				ev.SetBufId(bufID)
				return ev.SetPath(path)
			})
			defer rel()
			fut.Struct() //nolint:errcheck
		}(h)
	}
}

// DispatchBufferChange fires onChange for all plugins with a buffer handler.
func (m *Manager) DispatchBufferChange(ctx context.Context, bufID uint32, path string) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		p.mu.RLock()
		h := p.bufHandler
		p.mu.RUnlock()
		if !h.IsValid() {
			continue
		}
		go func(h pluginproto.BufferEventHandler) {
			fut, rel := h.OnChange(ctx, func(ps pluginproto.BufferEventHandler_onChange_Params) error {
				ev, err := ps.NewEvent()
				if err != nil {
					return err
				}
				ev.SetBufId(bufID)
				return ev.SetPath(path)
			})
			defer rel()
			fut.Struct() //nolint:errcheck
		}(h)
	}
}

// DispatchBufferSave fires onSave for all plugins with a buffer handler.
func (m *Manager) DispatchBufferSave(ctx context.Context, bufID uint32, path string) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		p.mu.RLock()
		h := p.bufHandler
		p.mu.RUnlock()
		if !h.IsValid() {
			continue
		}
		go func(h pluginproto.BufferEventHandler) {
			fut, rel := h.OnSave(ctx, func(ps pluginproto.BufferEventHandler_onSave_Params) error {
				ev, err := ps.NewEvent()
				if err != nil {
					return err
				}
				ev.SetBufId(bufID)
				return ev.SetPath(path)
			})
			defer rel()
			fut.Struct() //nolint:errcheck
		}(h)
	}
}

// DispatchBufferClose fires onClose for all plugins with a buffer handler.
func (m *Manager) DispatchBufferClose(ctx context.Context, bufID uint32, path string) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		p.mu.RLock()
		h := p.bufHandler
		p.mu.RUnlock()
		if !h.IsValid() {
			continue
		}
		go func(h pluginproto.BufferEventHandler) {
			fut, rel := h.OnClose(ctx, func(ps pluginproto.BufferEventHandler_onClose_Params) error {
				ev, err := ps.NewEvent()
				if err != nil {
					return err
				}
				ev.SetBufId(bufID)
				return ev.SetPath(path)
			})
			defer rel()
			fut.Struct() //nolint:errcheck
		}(h)
	}
}

func pluginsConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "indigo", "plugins"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "indigo", "plugins"), nil
}

func (m *Manager) pluginSocketPath(pluginName string) string {
	h := sha256.Sum256([]byte(m.workDir))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("indigo-%x", h[:8]))
	os.MkdirAll(dir, 0o700) //nolint:errcheck
	return filepath.Join(dir, "plugin-"+pluginName+".sock")
}

func loadManifest(pluginDir string) (*PluginToml, error) {
	var manifest PluginToml
	if _, err := toml.DecodeFile(filepath.Join(pluginDir, "plugin.toml"), &manifest); err != nil {
		return nil, err
	}
	if manifest.Name == "" {
		return nil, fmt.Errorf("plugin.toml missing name")
	}
	return &manifest, nil
}

func selectBinary(pluginDir string, manifest *PluginToml) (string, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	rel, ok := manifest.Binaries[key]
	if !ok {
		return "", fmt.Errorf("no binary for %s", key)
	}
	abs := filepath.Join(pluginDir, rel)
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
