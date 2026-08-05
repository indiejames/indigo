// Package lint runs configured/auto-detected external linters and normalizes
// their output into lsp.Diagnostic, so it can be merged alongside whatever
// the file's LSP server already reports. It deliberately has no LSP
// fallback (unlike internal/format) — when no linter matches an extension,
// there's simply nothing extra to add on top of the LSP's own diagnostics.
package lint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/lsp"
)

// Manager picks the right linter for a given file and runs it asynchronously,
// caching the most recently completed run's results per path.
type Manager struct {
	userLints []config.LinterConfig
	autoLints []config.LinterConfig
	workDir   string

	mu      sync.Mutex
	cached  map[string][]lsp.Diagnostic // path -> most recently completed run
	running map[string]bool             // path -> a run is currently in flight
	pending map[string]bool             // path -> re-run once the in-flight one finishes
}

// NewManager builds a Manager. autoLints are detected once at startup by
// scanning PATH and <workDir>/node_modules/.bin/, mirroring format.Manager.
func NewManager(cfg *config.Config, workDir string) *Manager {
	m := &Manager{
		userLints: cfg.Linters,
		workDir:   workDir,
		cached:    make(map[string][]lsp.Diagnostic),
		running:   make(map[string]bool),
		pending:   make(map[string]bool),
	}
	localBin := filepath.Join(workDir, "node_modules", ".bin")
	for _, d := range config.DefaultLinters {
		cmd := expandPath(d.Command)
		if _, err := exec.LookPath(cmd); err == nil {
			m.autoLints = append(m.autoLints, d)
			continue
		}
		local := filepath.Join(localBin, filepath.Base(cmd))
		if _, err := os.Stat(local); err == nil {
			localLC := d
			localLC.Command = local
			m.autoLints = append(m.autoLints, localLC)
		}
	}
	return m
}

// findLinter returns the configured/auto-detected linter for ext, user
// config taking precedence over the built-in defaults.
func (m *Manager) findLinter(ext string) (config.LinterConfig, bool) {
	for _, l := range m.userLints {
		if matchesExt(l.Extensions, ext) {
			return l, true
		}
	}
	for _, l := range m.autoLints {
		if matchesExt(l.Extensions, ext) {
			return l, true
		}
	}
	return config.LinterConfig{}, false
}

// RunAsync runs the configured/auto-detected linter for path in the
// background (reading the file's on-disk content, so callers should invoke
// this after the save that should be linted has been written). If a run for
// path is already in flight, this call is coalesced into it: the in-flight
// run triggers exactly one more run after it completes, rather than the
// caller's request being dropped or a second process racing the first.
func (m *Manager) RunAsync(path string) {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	lc, ok := m.findLinter(ext)
	if !ok {
		return
	}

	m.mu.Lock()
	if m.running[path] {
		m.pending[path] = true
		m.mu.Unlock()
		return
	}
	m.running[path] = true
	m.mu.Unlock()

	go m.run(path, lc)
}

func (m *Manager) run(path string, lc config.LinterConfig) {
	diags, err := runLinter(lc, path, m.workDir)

	m.mu.Lock()
	if err == nil {
		m.cached[path] = diags
	}
	m.running[path] = false
	rerun := m.pending[path]
	m.pending[path] = false
	m.mu.Unlock()

	if rerun {
		m.RunAsync(path)
	}
}

// GetDiagnostics returns the most recently completed lint run's results for
// path (possibly stale by one save); it never blocks on a run in progress.
func (m *Manager) GetDiagnostics(path string) []lsp.Diagnostic {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cached[path]
}

// runLinter executes lc's command against filePath and parses its output.
// The process runs with workDir as its working directory — cargo clippy in
// particular discovers Cargo.toml from the CWD rather than from a
// command-line argument, so without this it fails outright on any project
// not launched from its own root. Most linters exit non-zero when they find
// issues, so a non-zero exit is only treated as a fatal error when there's
// no parseable output to fall back on.
func runLinter(lc config.LinterConfig, filePath, workDir string) ([]lsp.Diagnostic, error) {
	parse, ok := parsers[lc.Format]
	if !ok {
		return nil, fmt.Errorf("lint: unknown format %q for %s", lc.Format, lc.Command)
	}

	cmd := expandPath(lc.Command)
	args := expandArgs(lc.Args, filePath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proc := exec.CommandContext(ctx, cmd, args...)
	proc.Dir = workDir
	var out bytes.Buffer
	proc.Stdout = &out
	runErr := proc.Run()

	// Check for context timeout/cancellation before parsing partial output
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	diags, parseErr := parse(out.Bytes(), filePath)
	if parseErr != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%s: %w", lc.Command, runErr)
		}
		return nil, parseErr
	}
	return diags, nil
}

func matchesExt(extensions []string, ext string) bool {
	for _, e := range extensions {
		if e == ext {
			return true
		}
	}
	return false
}

// expandPath expands a leading ~/ to the user's home directory.
func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func expandArgs(args []string, filePath string) []string {
	if len(args) == 0 {
		return nil
	}
	expanded := make([]string, len(args))
	for i, a := range args {
		expanded[i] = strings.ReplaceAll(a, "{file}", filePath)
	}
	return expanded
}
