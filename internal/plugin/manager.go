package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
	// PluginShowMessage shows text in the status bar of one client, or of
	// every connected client when clientID is 0.
	PluginShowMessage(clientID uint64, text string)
	PluginRunProcess(cmd string, args []string) (stdout, stderr string, exitCode int32, err error)

	// Plugin-driven UI — generic popup and text-input overlays.
	PluginShowPopup(title string, items []PluginPopupItem, onSelect func(data string), onCancel func())
	PluginShowInputPrompt(title, placeholder string, onConfirm func(text string), onCancel func())

	// Key registration notification — called so the server can push to clients.
	PluginKeyRegistered(trigger string)

	// PluginOpenBuffers returns all currently-open (bufID, path) pairs so plugins
	// can receive OnOpen for buffers that were opened before the plugin started.
	PluginOpenBuffers() []PluginBufferRef
}

// PluginBufferRef identifies an open buffer by ID and path.
type PluginBufferRef struct {
	BufID uint32
	Path  string
}

// PluginDecorationKind mirrors the capnp enum for use in plain-Go types.
type PluginDecorationKind int

const (
	DecorationKindGutter     PluginDecorationKind = 0
	DecorationKindOverlay    PluginDecorationKind = 1
	DecorationKindStatusBar  PluginDecorationKind = 2
	DecorationKindUnderline  PluginDecorationKind = 3
	DecorationKindLeftGutter PluginDecorationKind = 4
)

// PluginPopupItem is one entry in a plugin-driven list popup.
type PluginPopupItem struct {
	Label    string
	Sublabel string
	Data     string // opaque token returned to the plugin on selection
}

// PluginUnderlineStyle mirrors the capnp UnderlineStyle enum.
type PluginUnderlineStyle int

const (
	PluginUnderlineNone     PluginUnderlineStyle = 0
	PluginUnderlineStraight PluginUnderlineStyle = 1
	PluginUnderlineCurly    PluginUnderlineStyle = 2
)

// PluginDecoration is a single decoration returned by a plugin's DecorationProvider.
type PluginDecoration struct {
	Line uint32
	Col  uint32
	Text string
	Kind PluginDecorationKind

	// Underline fields (Kind == DecorationKindUnderline)
	EndCol         uint32
	UnderlineStyle PluginUnderlineStyle
	UnderlineColor string

	// Fix fields
	Fixable  bool
	FixData  string
	PluginName string // which plugin owns this decoration (for fix routing)

	// TextColor is the hex foreground color for gutter/overlay text; empty = default.
	TextColor string
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
	SdkVersion  string            `toml:"sdk_version"` // stored but not validated yet
	Description string            `toml:"description"`
	Binaries    map[string]string `toml:"binaries"`
	// Hashes maps the same os/arch keys as Binaries to expected SHA-256 hashes
	// in the form "sha256:<hex>". When present, the binary is rejected if its
	// hash does not match. Manifests that omit this field skip hash checking.
	Hashes map[string]string `toml:"hashes"`
	// TriggerKey overrides the key the plugin registers as its trigger.
	// Only applies when the plugin registers exactly one key binding.
	// Example: trigger_key = "r"
	TriggerKey string `toml:"trigger_key"`
	// KeyDescription is a short description of the trigger key shown in the ? help popup.
	// If omitted, the plugin's description field is used (truncated).
	KeyDescription string `toml:"key_description"`
}

// registeredPlugin holds a running plugin's process, RPC connection, and
// the handlers it registered during initialize.
type registeredPlugin struct {
	name           string
	description    string // from manifest description field
	keyDescription string // from manifest key_description field; empty = use description
	process        *os.Process
	rpcConn        *rpc.Conn

	mu             sync.RWMutex
	keyBindings    map[string]pluginproto.KeyHandler
	insertHooks    map[string]pluginproto.KeyHandler
	commands       map[string]pluginproto.CommandHandler
	bufHandler     pluginproto.BufferEventHandler
	decorProvider  pluginproto.DecorationProvider
	actionProvider pluginproto.ActionProvider
	editHandler    pluginproto.EditEventHandler
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
	p.actionProvider.Release()
	p.editHandler.Release()
}

// Manager discovers plugins in the user's plugins directory and manages their
// processes for the lifetime of one workspace session.
type Manager struct {
	mu      sync.Mutex
	plugins []*registeredPlugin
	workDir string
	bridge  ServerBridge

	// capture state: when a plugin returns captureKeys > 0, subsequent keys
	// with mode "capture" are routed to this handler instead of looking up by name.
	captureMu      sync.Mutex
	captureHandler pluginproto.KeyHandler
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
			pluginLog("plugin %s: manifest error: %v", entry.Name(), err)
			continue
		}
		binaryPath, err := selectBinary(pluginDir, manifest)
		if err != nil {
			pluginLog("plugin %s: binary error: %v", manifest.Name, err)
			continue
		}
		if err := m.startPlugin(ctx, manifest, binaryPath); err != nil {
			pluginLog("plugin %s: start error: %v", manifest.Name, err)
			continue
		}
		pluginLog("plugin %s: started OK (%s)", manifest.Name, binaryPath)
	}
	return nil
}

func pluginLog(format string, args ...any) {
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	fmt.Fprintf(f, format+"\n", args...) //nolint:errcheck
}

func pluginLogFile() *os.File {
	path := filepath.Join(os.TempDir(), "indigo-plugins.log")
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	return f
}

func (m *Manager) startPlugin(ctx context.Context, manifest *PluginToml, binaryPath string) error {
	name := manifest.Name
	sockPath := m.pluginSocketPath(name)
	os.Remove(sockPath) //nolint:errcheck

	logFile := pluginLogFile()
	var errFile *os.File
	if logFile != nil {
		errFile = logFile
	}
	proc, err := os.StartProcess(binaryPath,
		[]string{binaryPath, "--socket", sockPath},
		&os.ProcAttr{Files: []*os.File{nil, nil, errFile}},
	)
	if logFile != nil {
		logFile.Close() //nolint:errcheck
	}
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
		name:           name,
		description:    manifest.Description,
		keyDescription: manifest.KeyDescription,
		process:        proc,
		keyBindings:    make(map[string]pluginproto.KeyHandler),
		insertHooks:    make(map[string]pluginproto.KeyHandler),
		commands:       make(map[string]pluginproto.CommandHandler),
	}

	apiServer := &editorApiServer{reg: reg, bridge: m.bridge}
	api := pluginproto.EditorApi_ServerToClient(apiServer)
	defer api.Release()

	rpcConn := rpc.NewConn(rpc.NewStreamTransport(netConn), &rpc.Options{
		BootstrapClient: capnp.Client(api).AddRef(),
	})
	reg.rpcConn = rpcConn

	plugin := pluginproto.Plugin(rpcConn.Bootstrap(ctx))
	defer plugin.Release()

	fut, rel := plugin.Initialize(ctx, func(p pluginproto.Plugin_initialize_Params) error {
		return p.SetApi(pluginproto.EditorApi(capnp.Client(api).AddRef()))
	})
	defer rel()

	if _, err := fut.Struct(); err != nil {
		rpcConn.Close() //nolint:errcheck
		proc.Kill()     //nolint:errcheck
		return fmt.Errorf("plugin %s initialize: %w", name, err)
	}

	// If trigger_key is set and the plugin registered exactly one key, remap it.
	if manifest.TriggerKey != "" {
		reg.mu.Lock()
		if len(reg.keyBindings) == 1 {
			for oldKey, h := range reg.keyBindings {
				if oldKey != manifest.TriggerKey {
					delete(reg.keyBindings, oldKey)
					reg.keyBindings[manifest.TriggerKey] = h
					if m.bridge != nil {
						m.bridge.PluginKeyRegistered(manifest.TriggerKey)
					}
				}
			}
		}
		reg.mu.Unlock()
	}

	m.mu.Lock()
	m.plugins = append(m.plugins, reg)
	m.mu.Unlock()
	return nil
}

// AllRegisteredKeys returns every key trigger registered across all loaded plugins.
func (m *Manager) AllRegisteredKeys() []string {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	var keys []string
	for _, p := range plugins {
		p.mu.RLock()
		for k := range p.keyBindings {
			keys = append(keys, k)
		}
		p.mu.RUnlock()
	}
	return keys
}

// PluginBinding describes one key binding contributed by a plugin.
type PluginBinding struct {
	PluginName  string
	Key         string
	Description string // short description for the help popup
}

// AllPluginBindings returns all key bindings contributed by loaded plugins,
// with the plugin name and a short description for each.
func (m *Manager) AllPluginBindings() []PluginBinding {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	var bindings []PluginBinding
	for _, p := range plugins {
		p.mu.RLock()
		desc := p.keyDescription
		if desc == "" {
			desc = p.description
		}
		for k := range p.keyBindings {
			bindings = append(bindings, PluginBinding{
				PluginName:  p.name,
				Key:         k,
				Description: desc,
			})
		}
		p.mu.RUnlock()
	}
	return bindings
}

// GetDecorations calls every registered DecorationProvider and aggregates the results.
// startLine and endLine are the inclusive visible range (0-based buffer line numbers).
func (m *Manager) GetDecorations(ctx context.Context, clientID uint64, bufID, startLine, endLine uint32) []PluginDecoration {
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

		tctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		fut, rel := provider.GetDecorations(tctx, func(ps pluginproto.DecorationProvider_getDecorations_Params) error {
			ps.SetBufId(bufID)
			ps.SetClientId(clientID)
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
		cancel()
		if err != nil {
			rel()
			continue
		}
		rawList, err := res.Decorations()
		if err != nil {
			rel()
			continue
		}
		for i := range rawList.Len() {
			item := rawList.At(i)
			text, _ := item.Text()
			color, _ := item.UnderlineColor()
			fixData, _ := item.FixData()
			textColor, _ := item.TextColor()
			all = append(all, PluginDecoration{
				Line:           item.Line(),
				Col:            item.Col(),
				Text:           text,
				Kind:           PluginDecorationKind(item.Kind()),
				EndCol:         item.EndCol(),
				UnderlineStyle: PluginUnderlineStyle(item.UnderlineStyle()),
				UnderlineColor: color,
				Fixable:        item.Fixable(),
				FixData:        fixData,
				PluginName:     p.name,
				TextColor:      textColor,
			})
		}
		rel()
	}
	return all
}

// GetFixes calls the DecorationProvider of the named plugin to fetch fix options.
func (m *Manager) GetFixes(ctx context.Context, pluginName, fixData string) ([]FixItem, error) {
	m.mu.Lock()
	var provider pluginproto.DecorationProvider
	for _, p := range m.plugins {
		if p.name == pluginName {
			p.mu.RLock()
			provider = p.decorProvider
			p.mu.RUnlock()
			break
		}
	}
	m.mu.Unlock()

	if !provider.IsValid() {
		return nil, nil
	}
	tctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	fut, rel := provider.GetFixes(tctx, func(ps pluginproto.DecorationProvider_getFixes_Params) error {
		return ps.SetFixData(fixData)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	rawList, err := res.Items()
	if err != nil {
		return nil, err
	}
	items := make([]FixItem, rawList.Len())
	for i := range rawList.Len() {
		fi := rawList.At(i)
		label, _ := fi.Label()
		replace, _ := fi.Replace()
		items[i] = FixItem{Label: label, Replace: replace}
	}
	return items, nil
}

// ApplyFix calls the DecorationProvider of the named plugin to execute a custom fix.
func (m *Manager) ApplyFix(ctx context.Context, pluginName, fixData string, index uint32) error {
	m.mu.Lock()
	var provider pluginproto.DecorationProvider
	for _, p := range m.plugins {
		if p.name == pluginName {
			p.mu.RLock()
			provider = p.decorProvider
			p.mu.RUnlock()
			break
		}
	}
	m.mu.Unlock()

	if !provider.IsValid() {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	fut, rel := provider.ApplyFix(tctx, func(ps pluginproto.DecorationProvider_applyFix_Params) error {
		ps.SetIndex(index)
		return ps.SetFixData(fixData)
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// FixItem is a fix option returned by GetFixes.
type FixItem struct {
	Label   string
	Replace string // non-empty = direct text replacement; empty = call ApplyFix
}

// ActionItem is one context-sensitive action returned by an ActionProvider.
type ActionItem struct {
	Label      string
	Replace    string // non-empty = insert this text after deleting [FromLine:FromCol, ToLine:ToCol)
	FromLine   uint32
	FromCol    uint32
	ToLine     uint32
	ToCol      uint32
	PluginName string // which plugin owns this item (for callbacks)
}

// GetActionsAt calls all registered action providers for (bufID, line, col)
// and returns the merged list of actions.
func (m *Manager) GetActionsAt(ctx context.Context, bufID, line, col uint32) ([]ActionItem, error) {
	m.mu.Lock()
	providers := make([]struct {
		name     string
		provider pluginproto.ActionProvider
	}, 0, len(m.plugins))
	for _, p := range m.plugins {
		p.mu.RLock()
		if p.actionProvider.IsValid() {
			providers = append(providers, struct {
				name     string
				provider pluginproto.ActionProvider
			}{name: p.name, provider: p.actionProvider.AddRef()})
		}
		p.mu.RUnlock()
	}
	m.mu.Unlock()

	var all []ActionItem
	for _, entry := range providers {
		tctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		fut, rel := entry.provider.GetActions(tctx, func(ps pluginproto.ActionProvider_getActions_Params) error {
			ps.SetBufId(bufID)
			ps.SetLine(line)
			ps.SetCol(col)
			return nil
		})
		res, err := fut.Struct()
		cancel()
		rel()
		entry.provider.Release()
		if err != nil {
			continue
		}
		rawList, err := res.Items()
		if err != nil {
			continue
		}
		for i := range rawList.Len() {
			it := rawList.At(i)
			label, _ := it.Label()
			replace, _ := it.Replace()
			all = append(all, ActionItem{
				Label:      label,
				Replace:    replace,
				FromLine:   it.FromLine(),
				FromCol:    it.FromCol(),
				ToLine:     it.ToLine(),
				ToCol:      it.ToCol(),
				PluginName: entry.name,
			})
		}
	}
	return all, nil
}

// ApplyAction calls the named plugin's ActionProvider.applyAction.
func (m *Manager) ApplyAction(ctx context.Context, pluginName string, bufID, line, col, index uint32) error {
	m.mu.Lock()
	var provider pluginproto.ActionProvider
	for _, p := range m.plugins {
		if p.name == pluginName {
			p.mu.RLock()
			provider = p.actionProvider
			p.mu.RUnlock()
			break
		}
	}
	m.mu.Unlock()

	if !provider.IsValid() {
		return nil
	}
	tctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	fut, rel := provider.ApplyAction(tctx, func(ps pluginproto.ActionProvider_applyAction_Params) error {
		ps.SetBufId(bufID)
		ps.SetLine(line)
		ps.SetCol(col)
		ps.SetIndex(index)
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}

// DispatchEditEvent fires linesChanged on all plugins that registered an edit handler.
// Called after any op that changes the document line count (lineDelta != 0).
func (m *Manager) DispatchEditEvent(ctx context.Context, bufID uint32, filePath string, atLine uint32, lineDelta int32) {
	m.mu.Lock()
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		p.mu.RLock()
		h := p.editHandler
		p.mu.RUnlock()
		if !h.IsValid() {
			continue
		}
		go func(h pluginproto.EditEventHandler) {
			fut, rel := h.LinesChanged(ctx, func(ps pluginproto.EditEventHandler_linesChanged_Params) error {
				ps.SetBufId(bufID)
				if err := ps.SetFilePath(filePath); err != nil {
					return err
				}
				ps.SetAtLine(atLine)
				ps.SetLineDelta(lineDelta)
				return nil
			})
			defer rel()
			fut.Struct() //nolint:errcheck
		}(h)
	}
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
			p.rpcConn.Close() //nolint:errcheck
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
	m.clearCaptureHandler()
}

// HandleKey routes a keypress to the appropriate plugin handler.
//
// In "capture" mode the key goes to whichever handler last returned captureKeys>0,
// regardless of the key name. In all other modes the key is looked up by name in
// each plugin's registered key bindings.
//
// Plugin handlers are called with a 30 ms deadline; timeout → handled=false.
func (m *Manager) HandleKey(ctx context.Context, key, mode string, bufID uint32, clientID uint64, curLine, curCol uint32) (
	handled bool, edits []TextEdit, cursorLine, cursorCol uint32, hasCursor bool, captureKeys uint32, err error,
) {
	if mode == "capture" {
		m.captureMu.Lock()
		h := m.captureHandler
		m.captureMu.Unlock()
		if !h.IsValid() {
			return false, nil, 0, 0, false, 0, nil
		}
		handled, edits, cursorLine, cursorCol, hasCursor, captureKeys, err = m.callHandler(ctx, h, key, mode, bufID, clientID, curLine, curCol)
		if err == nil && captureKeys == 0 {
			m.clearCaptureHandler()
		}
		return
	}

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
		handled, edits, cursorLine, cursorCol, hasCursor, captureKeys, err = m.callHandler(ctx, handler, key, mode, bufID, clientID, curLine, curCol)
		if err != nil || !handled {
			return
		}
		if captureKeys > 0 {
			m.captureMu.Lock()
			m.captureHandler.Release()
			m.captureHandler = handler.AddRef()
			m.captureMu.Unlock()
		}
		return
	}
	return false, nil, 0, 0, false, 0, nil
}

// callHandler invokes a KeyHandler with a 30 ms deadline and unpacks the response.
func (m *Manager) callHandler(ctx context.Context, handler pluginproto.KeyHandler, key, mode string, bufID uint32, clientID uint64, curLine, curCol uint32) (
	handled bool, edits []TextEdit, cursorLine, cursorCol uint32, hasCursor bool, captureKeys uint32, err error,
) {
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()

	fut, rel := handler.Handle(tctx, func(ps pluginproto.KeyHandler_handle_Params) error {
		if err := ps.SetKey(key); err != nil {
			return err
		}
		kctx, err := ps.NewCtx()
		if err != nil {
			return err
		}
		kctx.SetBufId(bufID)
		kctx.SetClientId(clientID)
		kctx.SetCursorLine(curLine)
		kctx.SetCursorCol(curCol)
		return kctx.SetMode(mode)
	})
	defer rel()

	res, callErr := fut.Struct()
	if callErr != nil {
		return false, nil, 0, 0, false, 0, nil
	}
	resp, respErr := res.Response()
	if respErr != nil {
		return false, nil, 0, 0, false, 0, respErr
	}
	if !resp.Handled() {
		return false, nil, 0, 0, false, 0, nil
	}

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

func (m *Manager) clearCaptureHandler() {
	m.captureMu.Lock()
	m.captureHandler.Release()
	m.captureHandler = pluginproto.KeyHandler{}
	m.captureMu.Unlock()
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
	// Match the server's directory naming: include UID to isolate per-user.
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("indigo-%d-%x", os.Getuid(), h[:8]))
	os.MkdirAll(dir, 0o700)  //nolint:errcheck
	os.Chmod(dir, 0o700)     //nolint:errcheck
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
	if expected, ok := manifest.Hashes[key]; ok {
		if err := verifyBinaryHash(abs, expected); err != nil {
			return "", fmt.Errorf("plugin binary integrity check failed: %w", err)
		}
	}
	return abs, nil
}

// verifyBinaryHash checks that the file at path matches expected, which must be
// in the form "sha256:<lowercase-hex>". Returns an error if the hash does not
// match or the format is unrecognised.
func verifyBinaryHash(path, expected string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(expected, prefix) {
		return fmt.Errorf("unsupported hash format %q (want sha256:<hex>)", expected)
	}
	want, err := hex.DecodeString(expected[len(prefix):])
	if err != nil {
		return fmt.Errorf("invalid hash hex: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("hash mismatch for %s", filepath.Base(path))
	}
	return nil
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
