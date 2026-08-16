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
	"github.com/indiejames/indigo/internal/procutil"
)

// Manager picks the right linter for a given file and runs it asynchronously,
// caching the most recently completed run's results per path.
type Manager struct {
	userLints []config.LinterConfig
	autoLints []config.LinterConfig
	workDir   string

	mu          sync.Mutex
	cached      map[string][]lsp.Diagnostic // path -> most recently completed run
	running     map[string]bool             // path -> a run is currently in flight
	pending     map[string]bool             // path -> re-run once the in-flight one finishes
	content     map[string]string           // path -> content of the most recent (possibly still in-flight) request
	lastErr     map[string]error            // path -> error from the most recently completed run, nil once one succeeds
	activeToken map[string]uint64           // path -> token of the run currently allowed to write results for it
	tokenSeq    uint64                      // monotonically increasing source for activeToken values
}

// NewManager builds a Manager. autoLints are detected once at startup by
// scanning PATH and <workDir>/node_modules/.bin/, mirroring format.Manager.
func NewManager(cfg *config.Config, workDir string) *Manager {
	m := &Manager{
		userLints:   cfg.Linters,
		workDir:     workDir,
		cached:      make(map[string][]lsp.Diagnostic),
		running:     make(map[string]bool),
		pending:     make(map[string]bool),
		content:     make(map[string]string),
		lastErr:     make(map[string]error),
		activeToken: make(map[string]uint64),
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
// background against content. Linters that read from disk instead of stdin
// (see LinterConfig.Stdin) ignore content and read the file directly, so
// callers on that path should invoke this only after the save that should
// be linted has been written. If a run for path is already in flight, this
// call is coalesced into it: the in-flight run triggers exactly one more
// run after it completes (using the latest content passed in, not the
// content at the time the in-flight run started), rather than the caller's
// request being dropped or a second process racing the first.
func (m *Manager) RunAsync(path, content string) {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	lc, ok := m.findLinter(ext)
	if !ok {
		return
	}
	m.runAsync(path, content, lc)
}

// RunOnEdit is RunAsync's live-typing counterpart: it only does anything for
// linters that can lint in-memory content via stdin (LinterConfig.Stdin).
// Disk-reading linters (golangci-lint, cargo clippy) are compile-based and
// need a real on-disk project to run against, so re-invoking them on every
// keystroke would just repeat the same stale-disk-content run for no
// benefit — those stay save-triggered via RunAsync.
func (m *Manager) RunOnEdit(path, content string) {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	lc, ok := m.findLinter(ext)
	if !ok || !lc.Stdin {
		return
	}
	m.runAsync(path, content, lc)
}

func (m *Manager) runAsync(path, content string, lc config.LinterConfig) {
	m.mu.Lock()
	m.content[path] = content
	if m.running[path] {
		m.pending[path] = true
		m.mu.Unlock()
		return
	}
	m.running[path] = true
	m.tokenSeq++
	token := m.tokenSeq
	m.activeToken[path] = token
	m.mu.Unlock()

	go m.run(path, lc, token)
}

// run executes lc against path and, if token is still the path's current
// activeToken, records the result. token stops being current when Forget
// invalidates it (file closed mid-run) or a newer run has since claimed the
// path — in either case this run's result is discarded without touching
// running/pending/content, since a run that owns neither of those maps'
// entries anymore must not perturb whichever run (if any) does.
func (m *Manager) run(path string, lc config.LinterConfig, token uint64) {
	m.mu.Lock()
	content := m.content[path]
	m.mu.Unlock()

	diags, err := runLinter(lc, path, content, m.workDir)

	m.mu.Lock()
	if cur, ok := m.activeToken[path]; !ok || cur != token {
		m.mu.Unlock()
		return
	}
	if err == nil {
		m.cached[path] = diags
		delete(m.lastErr, path)
	} else {
		m.lastErr[path] = err
	}
	m.running[path] = false
	rerun := m.pending[path]
	m.pending[path] = false
	var latest string
	if rerun {
		latest = m.content[path]
	} else {
		// No rerun queued: content was only needed to feed this run's
		// stdin, so drop it rather than holding the file's full text in
		// memory for the rest of the session.
		delete(m.content, path)
	}
	m.mu.Unlock()

	if rerun {
		m.runAsync(path, latest, lc)
	}
}

// GetDiagnostics returns the most recently completed lint run's results for
// path (possibly stale by one save); it never blocks on a run in progress.
func (m *Manager) GetDiagnostics(path string) []lsp.Diagnostic {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cached[path]
}

// LastError returns the error from path's most recently completed run, or
// nil if that run succeeded (or none has completed yet). GetDiagnostics
// alone can't distinguish "the linter found nothing" from "the linter has
// been failing/timing out and cached is just whatever it last managed to
// produce" — a linter that repeatedly fails otherwise leaves indefinitely
// stale diagnostics with no visible sign anything is wrong.
func (m *Manager) LastError(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[path]
}

// Forget discards all state Manager holds for path (cached diagnostics, the
// error from its last run, and the running/pending/content in-flight
// bookkeeping). Callers should invoke this once a file is closed for good
// (no clients have it open anymore) — otherwise every path ever linted in
// the session accumulates an entry in these maps forever, even though the
// values themselves (a `false` running/pending flag) are individually
// small. Safe to call on a path with a run currently in flight: deleting
// activeToken[path] means that run's token no longer matches when it
// eventually completes, so run() discards its result instead of
// repopulating a fresh entry for path or, worse, clobbering a newer run's
// result if path was reopened and relinted before the old run finished.
func (m *Manager) Forget(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cached, path)
	delete(m.running, path)
	delete(m.pending, path)
	delete(m.content, path)
	delete(m.lastErr, path)
	delete(m.activeToken, path)
}

// runLinter executes lc's command against filePath and parses its output.
// The process runs with workDir as its working directory — cargo clippy in
// particular discovers Cargo.toml from the CWD rather than from a
// command-line argument, so without this it fails outright on any project
// not launched from its own root. Most linters exit non-zero when they find
// issues, so a non-zero exit is only treated as a fatal error when there's
// no parseable output to fall back on. When lc.Stdin is set, content is
// piped to the process's stdin instead of the linter reading filePath off
// disk itself, so this reflects the buffer's current (possibly unsaved)
// state rather than what was last written to disk.
func runLinter(lc config.LinterConfig, filePath, content, workDir string) ([]lsp.Diagnostic, error) {
	parse, ok := parsers[lc.Format]
	if !ok {
		return nil, fmt.Errorf("lint: unknown format %q for %s", lc.Format, lc.Command)
	}

	cmd := expandPath(lc.Command)
	args := expandArgs(lc.Args, filePath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proc := exec.CommandContext(ctx, cmd, args...)
	procutil.SetPgid(proc)
	proc.Cancel = func() error { return procutil.KillGroup(proc) }
	proc.Dir = workDir
	if lc.Stdin {
		proc.Stdin = strings.NewReader(content)
	}
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
