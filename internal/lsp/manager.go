package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ServerConfig describes how to launch a language server for a set of file extensions.
type ServerConfig struct {
	Extensions []string
	Command    string
	Args       []string
}

// Manager holds one Client per language, lazily started.
type Manager struct {
	mu          sync.Mutex
	clients     map[string]*Client // languageID → Client
	servers     []ServerConfig
	rootDir     string
	fileContent map[string]string // path → content stored by DidOpen for ensureOpened
}

// NewManager creates a Manager for the given workspace root.
// servers is the ordered list of language server configs; user-configured entries
// should come first so they shadow the built-in defaults.
func NewManager(rootDir string, servers []ServerConfig) *Manager {
	return &Manager{
		clients:     make(map[string]*Client),
		servers:     servers,
		rootDir:     rootDir,
		fileContent: make(map[string]string),
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

	// Fast path: client already running.
	m.mu.Lock()
	if c, ok := m.clients[langID]; ok {
		m.mu.Unlock()
		return c
	}
	m.mu.Unlock()

	// Slow path: start and initialize the server without holding the mutex so
	// diagnostics polls and other operations can proceed concurrently.
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
		return nil
	}
	if err := c.Initialize(); err != nil {
		c.Shutdown()
		return nil
	}

	// Store the client, guarding against two goroutines both reaching this point.
	m.mu.Lock()
	if existing, ok := m.clients[langID]; ok {
		// Another goroutine finished first; discard ours.
		m.mu.Unlock()
		c.Shutdown()
		return existing
	}
	m.clients[langID] = c
	m.mu.Unlock()
	return c
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

// CodeActions returns quick-fix and refactor actions for the cursor position.
func (m *Manager) CodeActions(path string, line, col int) ([]CodeAction, error) {
	if c := m.clientForPath(path); c != nil {
		m.ensureOpened(c, path)
		return c.CodeActions(path, line, col)
	}
	return nil, nil
}

// Format requests formatting for path from its language server.
// Returns (content, false, nil) when no server is configured or formatting is unsupported.
func (m *Manager) Format(path, content string) (string, bool, error) {
	if c := m.clientForPath(path); c != nil {
		return c.Format(path, content)
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
