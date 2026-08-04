package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ServerConfig describes how to launch a language server for a set of file extensions.
type ServerConfig struct {
	Extensions []string
	Command    string
	Args       []string
}

// startRetryCooldown is how long clientForPath waits before retrying a
// language server whose most recent start attempt failed (not found, or
// failed/timed out during initialize). Without this, a broken server gets
// re-spawned on every DidOpen/DidChange call — one per keystroke — each
// paying the full cost of a process spawn plus (on an initialize timeout)
// up to Client.Initialize's 30s deadline, all running concurrently as
// unbounded background goroutines.
const startRetryCooldown = time.Minute

// Manager holds one Client per language, lazily started.
type Manager struct {
	mu           sync.Mutex
	clients      map[string]*Client       // languageID → Client
	failedStarts map[string]time.Time     // languageID → time of last failed start attempt
	starting     map[string]chan struct{} // languageID → closed when the in-flight start finishes
	servers      []ServerConfig
	rootDir      string
	fileContent  map[string]string // path → content stored by DidOpen for ensureOpened
}

// NewManager creates a Manager for the given workspace root.
// servers is the ordered list of language server configs; user-configured entries
// should come first so they shadow the built-in defaults.
func NewManager(rootDir string, servers []ServerConfig) *Manager {
	return &Manager{
		clients:      make(map[string]*Client),
		failedStarts: make(map[string]time.Time),
		starting:     make(map[string]chan struct{}),
		servers:      servers,
		rootDir:      rootDir,
		fileContent:  make(map[string]string),
	}
}

// clientForPath returns the Client responsible for path, starting it if needed.
// Returns nil if no server is configured for the file's extension.
func (m *Manager) clientForPath(path string) *Client {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext == "" {
		return nil
	}

	var cfg *ServerConfig
	for i := range m.servers {
		for _, e := range m.servers[i].Extensions {
			if e == ext {
				cfg = &m.servers[i]
				break
			}
		}
		if cfg != nil {
			break
		}
	}
	if cfg == nil {
		return nil
	}

	langID := languageIDForExt(ext)

	for {
		m.mu.Lock()
		if c, ok := m.clients[langID]; ok {
			m.mu.Unlock()
			return c
		}
		if last, failed := m.failedStarts[langID]; failed && time.Since(last) < startRetryCooldown {
			m.mu.Unlock()
			return nil
		}
		if ch, inProgress := m.starting[langID]; inProgress {
			// Another goroutine is already starting this language server —
			// wait for it to finish, then re-check clients/failedStarts
			// above instead of racing it to spawn a second process.
			m.mu.Unlock()
			<-ch
			continue
		}
		ch := make(chan struct{})
		m.starting[langID] = ch
		m.mu.Unlock()

		c := m.startClient(langID, cfg)

		m.mu.Lock()
		delete(m.starting, langID)
		m.mu.Unlock()
		close(ch)
		return c
	}
}

// startClient spawns and initializes the language server for cfg, without
// holding m.mu, so diagnostics polls and other operations on other languages
// can proceed concurrently. Callers must hold the langID slot in m.starting.
func (m *Manager) startClient(langID string, cfg *ServerConfig) *Client {
	cmd := cfg.Command
	c, err := NewClient(cmd, cfg.Args, m.rootDir)
	if err != nil {
		// Also check <workDir>/node_modules/.bin/ for locally-installed servers.
		local := filepath.Join(m.rootDir, "node_modules", ".bin", filepath.Base(cmd))
		if info, statErr := os.Stat(local); statErr == nil && !info.IsDir() {
			c, err = NewClient(local, cfg.Args, m.rootDir)
		}
	}
	if err != nil {
		// Language server not installed — silently skip.
		m.recordFailedStart(langID)
		return nil
	}
	if err := c.Initialize(); err != nil {
		c.Shutdown()
		m.recordFailedStart(langID)
		return nil
	}

	m.mu.Lock()
	m.clients[langID] = c
	delete(m.failedStarts, langID)
	m.mu.Unlock()
	return c
}

func (m *Manager) recordFailedStart(langID string) {
	m.mu.Lock()
	m.failedStarts[langID] = time.Now()
	m.mu.Unlock()
}

// DidOpen notifies the appropriate language server that path was opened.
// Content is cached before the client is started so that ensureOpened can
// guarantee DidOpen is sent before any hover/complete/etc requests.
func (m *Manager) DidOpen(path, content string) {
	m.mu.Lock()
	m.fileContent[path] = content
	m.mu.Unlock()
	if c := m.clientForPath(path); c != nil {
		c.DidOpen(path, content) //nolint:errcheck
	}
}

// ensureOpened sends textDocument/didOpen for path if it has not been sent yet.
// Client.DidOpen is idempotent, so calling this multiple times is safe.
func (m *Manager) ensureOpened(c *Client, path string) {
	m.mu.Lock()
	content, ok := m.fileContent[path]
	m.mu.Unlock()
	if ok {
		c.DidOpen(path, content) //nolint:errcheck
	}
}

// DidChange notifies the appropriate language server of a content change.
func (m *Manager) DidChange(path, content string) {
	if c := m.clientForPath(path); c != nil {
		c.DidChange(path, content) //nolint:errcheck
	}
}

// DidSave notifies the appropriate language server that path was saved.
func (m *Manager) DidSave(path string) {
	if c := m.clientForPath(path); c != nil {
		c.DidSave(path) //nolint:errcheck
	}
}

// DidClose notifies the appropriate language server that path was closed.
func (m *Manager) DidClose(path string) {
	if c := m.clientForPath(path); c != nil {
		c.DidClose(path) //nolint:errcheck
	}
}

// Definition returns definition locations for the symbol at (line, col) in path.
func (m *Manager) Definition(path string, line, col int) ([]Location, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.Definition(path, line, col)
	}
	return nil, nil
}

// GetDiagnostics returns the current diagnostics for path.
func (m *Manager) GetDiagnostics(path string) []Diagnostic {
	if c := m.clientForPath(path); c != nil {
		return c.GetDiagnostics(path)
	}
	return nil
}

// HasClient reports whether an initialized language server client exists for path.
func (m *Manager) HasClient(path string) bool {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext == "" {
		return false
	}
	langID := languageIDForExt(ext)
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.clients[langID]
	return ok
}

// Hover returns hover information for path at (line, col).
func (m *Manager) Hover(path string, line, col int) (*Hover, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.Hover(path, line, col)
	}
	return nil, nil
}

// SignatureHelp returns signature help for path at (line, col).
func (m *Manager) SignatureHelp(path string, line, col int) (*SignatureHelp, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.SignatureHelp(path, line, col)
	}
	return nil, nil
}

// Complete returns completion items for path at (line, col).
func (m *Manager) Complete(path string, line, col int) ([]CompletionItem, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.Complete(path, line, col)
	}
	return nil, nil
}

// ResolveCompletion resolves a completion item for path, filling in lazily
// computed fields such as AdditionalTextEdits (the auto-import line). Returns
// the item unchanged if no server is configured for path.
func (m *Manager) ResolveCompletion(path string, item CompletionItem) (CompletionItem, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.ResolveCompletion(item)
	}
	return item, nil
}

// WorkspaceSymbols queries the language server for path's workspace for symbols matching query.
func (m *Manager) WorkspaceSymbols(path, query string) ([]SymbolInformation, error) {
	if c := m.clientForPath(path); c != nil {
		return c.WorkspaceSymbols(query)
	}
	return nil, nil
}

// DocumentSymbols returns flattened symbols for path.
func (m *Manager) DocumentSymbols(path string) ([]SymbolInformation, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.DocumentSymbols(path)
	}
	return nil, nil
}

// References returns all reference locations for the symbol at (line, col) in path.
func (m *Manager) References(path string, line, col int) ([]Location, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.References(path, line, col)
	}
	return nil, nil
}

// CodeActions returns quick-fix and refactor actions for the given range
// (equal start/end for a plain cursor position, or a selection's bounds).
func (m *Manager) CodeActions(path string, startLine, startCol, endLine, endCol int) ([]CodeAction, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.CodeActions(path, startLine, startCol, endLine, endCol)
	}
	return nil, nil
}

// InlayHints returns inlay hints for path within the given range (normally the
// client's visible viewport).
func (m *Manager) InlayHints(path string, startLine, startCol, endLine, endCol int) ([]InlayHint, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.InlayHints(path, startLine, startCol, endLine, endCol)
	}
	return nil, nil
}

// SemanticTokensRange returns decoded semantic tokens for path within the
// given range (normally the client's visible viewport).
func (m *Manager) SemanticTokensRange(path string, startLine, startCol, endLine, endCol int) ([]SemanticToken, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.SemanticTokensRange(path, startLine, startCol, endLine, endCol)
	}
	return nil, nil
}

// Rename renames the symbol at (line, col) in path via its language server,
// returning the resulting WorkspaceEdit (nil if unsupported or unavailable).
func (m *Manager) Rename(path string, line, col int, newName string) (*WorkspaceEdit, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.Rename(path, line, col, newName)
	}
	return nil, nil
}

// RenameAfterChange is like Rename, but first notifies the server of
// content's current state and performs the rename request atomically with
// that notification (see Client.RenameAfterChange) — used right after an
// edit that a rename immediately depends on, e.g. Extract Function's
// automatic follow-up rename of the server's default name.
func (m *Manager) RenameAfterChange(path, content string, line, col int, newName string) (*WorkspaceEdit, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.RenameAfterChange(path, content, line, col, newName)
	}
	return nil, nil
}

// Format requests formatting for path from its language server.
// Returns (content, false, nil) when no server is configured or formatting is unsupported.
func (m *Manager) Format(path, content string, opts FormattingOptions) (string, bool, error) {
	if c := m.clientForPath(path); c != nil {
		return c.Format(path, content, opts)
	}
	return content, false, nil
}

// Shutdown cleanly shuts down all running language servers.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = make(map[string]*Client)
	m.mu.Unlock()

	for _, c := range clients {
		c.Shutdown()
	}
}
