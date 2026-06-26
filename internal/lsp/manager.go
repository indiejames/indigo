package lsp

import (
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
	mu      sync.Mutex
	clients map[string]*Client // languageID → Client
	servers []ServerConfig
	rootDir string
}

// NewManager creates a Manager for the given workspace root.
// servers is the ordered list of language server configs; user-configured entries
// should come first so they shadow the built-in defaults.
func NewManager(rootDir string, servers []ServerConfig) *Manager {
	return &Manager{
		clients: make(map[string]*Client),
		servers: servers,
		rootDir: rootDir,
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
	m.mu.Lock()
	defer m.mu.Unlock()

	if c, ok := m.clients[langID]; ok {
		return c
	}

	c, err := NewClient(cfg.Command, cfg.Args, m.rootDir)
	if err != nil {
		// Language server not installed — silently skip.
		return nil
	}
	if err := c.Initialize(); err != nil {
		c.Shutdown()
		return nil
	}
	m.clients[langID] = c
	return c
}

// DidOpen notifies the appropriate language server that path was opened.
func (m *Manager) DidOpen(path, content string) {
	if c := m.clientForPath(path); c != nil {
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

// Hover returns hover information for path at (line, col).
func (m *Manager) Hover(path string, line, col int) (*Hover, error) {
	if c := m.clientForPath(path); c != nil {
		return c.Hover(path, line, col)
	}
	return nil, nil
}

// SignatureHelp returns signature help for path at (line, col).
func (m *Manager) SignatureHelp(path string, line, col int) (*SignatureHelp, error) {
	if c := m.clientForPath(path); c != nil {
		return c.SignatureHelp(path, line, col)
	}
	return nil, nil
}

// Complete returns completion items for path at (line, col).
func (m *Manager) Complete(path string, line, col int) ([]CompletionItem, error) {
	if c := m.clientForPath(path); c != nil {
		return c.Complete(path, line, col)
	}
	return nil, nil
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
