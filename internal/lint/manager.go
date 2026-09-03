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
	"github.com/indiejames/indigo/internal/localbin"
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

	// Workspace-wide scan state (Manager.ScanWorkspace), independent of the
	// per-buffer state above — it covers files that aren't open in any
	// buffer, which the maps above never touch. Guarded by workspaceMu
	// rather than mu since a scan can run concurrently with, and outlive,
	// any number of per-file lint runs.
	workspaceMu      sync.Mutex
	workspaceByCmd   map[string]map[string][]lsp.Diagnostic // linter command -> path -> most recently completed scan's diagnostics
	workspaceErrs    map[string]error                       // linter command -> error from its most recently completed scan, nil once one succeeds
	workspaceRunning bool
	workspacePending bool

	// scanCtx bounds every workspace-scan subprocess (runWorkspaceLinter),
	// separately from the per-file runLinter timeouts above: a scan can run
	// up to workspaceScanTimeout (2 minutes), long enough that a server
	// shutdown mid-scan would otherwise leave an orphaned linter process
	// running detached from indigo for the rest of that window. cancelScan
	// is called by Shutdown to tear it down promptly instead.
	scanCtx    context.Context
	cancelScan context.CancelFunc
}

// NewManager builds a Manager. autoLints are the DefaultLinters found on
// PATH at startup — a fixed, workspace-independent check. A locally
// installed (node_modules/.bin) linter is instead resolved lazily, per
// file, in findLinter below: which node_modules/.bin actually applies can
// differ per file in a monorepo with non-hoisted per-package installs, so
// it can't be decided once at startup — see format.Manager's identically-
// shaped fix for the same gap.
func NewManager(cfg *config.Config, workDir string) *Manager {
	scanCtx, cancelScan := context.WithCancel(context.Background())
	m := &Manager{
		userLints:      cfg.Linters,
		workDir:        workDir,
		cached:         make(map[string][]lsp.Diagnostic),
		running:        make(map[string]bool),
		pending:        make(map[string]bool),
		content:        make(map[string]string),
		lastErr:        make(map[string]error),
		activeToken:    make(map[string]uint64),
		workspaceByCmd: make(map[string]map[string][]lsp.Diagnostic),
		workspaceErrs:  make(map[string]error),
		scanCtx:        scanCtx,
		cancelScan:     cancelScan,
	}
	for _, d := range config.DefaultLinters {
		if _, err := exec.LookPath(expandPath(d.Command)); err == nil {
			m.autoLints = append(m.autoLints, d)
		}
	}
	return m
}

// findLinter returns the configured/auto-detected linter for path's
// extension, user config taking precedence over the built-in defaults, and
// a PATH match taking precedence over a node_modules/.bin one resolved by
// walking up from path's own directory (see internal/localbin.Resolve) —
// the entry closest to path wins, so a monorepo package with its own
// non-hoisted node_modules is found even when nothing is installed at the
// workspace root.
func (m *Manager) findLinter(path, ext string) (config.LinterConfig, bool) {
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
	for _, d := range config.DefaultLinters {
		if !matchesExt(d.Extensions, ext) {
			continue
		}
		cmd := expandPath(d.Command)
		local, ok := localbin.Resolve(filepath.Dir(path), m.workDir, filepath.Base(cmd))
		if !ok {
			continue
		}
		localLC := d
		localLC.Command = local
		return localLC, true
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
	lc, ok := m.findLinter(path, ext)
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
	lc, ok := m.findLinter(path, ext)
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

// Shutdown cancels scanCtx, tearing down (via KillGroup, same as a normal
// timeout — see runWorkspaceLinter) any workspace-scan subprocess still
// running rather than leaving it orphaned past the server's own exit.
// Per-file lint runs aren't covered: their individual 30s timeouts are
// short enough that this hasn't been worth the same treatment.
func (m *Manager) Shutdown() {
	m.cancelScan()
}

// workspaceScanTimeout bounds one workspace-scan linter invocation — longer
// than the per-file 30s timeout in runLinter since a whole-project run
// (`golangci-lint run ./...`, `eslint .`, ...) legitimately takes longer.
// A var, not a const, so tests can shrink it rather than paying the real
// timeout when exercising the hang path (mirrors format.Manager's
// externalFormatTimeout).
var workspaceScanTimeout = 120 * time.Second

// effectiveWorkspaceLinters returns the deduplicated (by Command) set of
// configured/auto-detected linters that support a workspace scan, i.e.
// have a non-empty WorkspaceArgs. User config takes precedence over
// defaults for a given command, same as findLinter, achieved here simply by
// visiting userLints first and skipping a command already seen.
func (m *Manager) effectiveWorkspaceLinters() []config.LinterConfig {
	seen := make(map[string]bool)
	var out []config.LinterConfig
	for _, l := range m.userLints {
		if len(l.WorkspaceArgs) == 0 || seen[l.Command] {
			continue
		}
		seen[l.Command] = true
		out = append(out, l)
	}
	for _, l := range m.autoLints {
		if len(l.WorkspaceArgs) == 0 || seen[l.Command] {
			continue
		}
		seen[l.Command] = true
		out = append(out, l)
	}
	return out
}

// ScanWorkspace runs every workspace-capable linter (see
// effectiveWorkspaceLinters) against the whole project in the background,
// so GetWorkspaceScanDiagnostics/WorkspaceScanSnapshot can serve results for
// files that aren't open in any buffer — the per-file cache above never
// covers those, since nothing ever calls RunAsync/RunOnEdit for a file
// nobody opened. Call this on server startup and in response to an
// explicit rescan request; it deliberately runs no more often than that
// (unlike RunOnEdit's per-keystroke cadence) since a whole-project lint
// invocation is far more expensive than a single-file one. A scan already
// in progress coalesces a new request into one more run right after it
// finishes, same coalescing shape runAsync uses per-path.
func (m *Manager) ScanWorkspace() {
	m.workspaceMu.Lock()
	if m.workspaceRunning {
		m.workspacePending = true
		m.workspaceMu.Unlock()
		return
	}
	m.workspaceRunning = true
	m.workspaceMu.Unlock()

	go m.runWorkspaceScan()
}

// runWorkspaceScan runs every workspace-capable linter concurrently (each
// targets a different toolchain, so there's no shared resource to
// serialize on) and, once all have finished, replaces each linter's slice
// of workspaceByCmd wholesale on success — a scan is authoritative for the
// whole project, so unlike the per-file cache there's no older result to
// merge with. A linter whose run fails keeps its previous (stale) results
// visible rather than being cleared, matching the "leave stale data visible
// until a fresh fetch replaces it" philosophy runLinter's caller already
// follows for the per-file cache; only its recorded error changes.
//
// A request that coalesced in while this run was already in flight
// (workspacePending) is handled by looping right here rather than
// unlocking, dropping workspaceRunning, and having ScanWorkspace kick off a
// fresh goroutine: that would leave a window, between the unlock below and
// the recursive ScanWorkspace call, where workspaceRunning reads false even
// though a follow-up run is about to start — during which a concurrent
// ScanWorkspace caller (e.g. another "r" press) would see no scan in
// progress and launch a second one that races the coalesced follow-up
// instead of being coalesced into it. Looping keeps workspaceRunning true
// for the entire handoff, so WorkspaceScanning() and ScanWorkspace's own
// running-check stay accurate throughout.
func (m *Manager) runWorkspaceScan() {
	for {
		linters := m.effectiveWorkspaceLinters()

		type scanResult struct {
			cmd   string
			diags map[string][]lsp.Diagnostic
			err   error
		}
		results := make(chan scanResult, len(linters))
		var wg sync.WaitGroup
		for _, lc := range linters {
			wg.Add(1)
			go func(lc config.LinterConfig) {
				defer wg.Done()
				diags, err := runWorkspaceLinter(m.scanCtx, lc, m.workDir)
				results <- scanResult{cmd: lc.Command, diags: diags, err: err}
			}(lc)
		}
		wg.Wait()
		close(results)

		m.workspaceMu.Lock()
		for r := range results {
			if r.err != nil {
				m.workspaceErrs[r.cmd] = r.err
				continue
			}
			delete(m.workspaceErrs, r.cmd)
			m.workspaceByCmd[r.cmd] = r.diags
		}
		if !m.workspacePending {
			m.workspaceRunning = false
			m.workspaceMu.Unlock()
			return
		}
		m.workspacePending = false
		m.workspaceMu.Unlock()
	}
}

// WorkspaceScanDiagnostics returns path's diagnostics from the most
// recently completed workspace scan, merged across every linter that
// reported something for it (normally just one, keyed by extension, but
// nothing stops two configured linters from covering the same file).
func (m *Manager) WorkspaceScanDiagnostics(path string) []lsp.Diagnostic {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	var out []lsp.Diagnostic
	for _, byPath := range m.workspaceByCmd {
		out = append(out, byPath[path]...)
	}
	return out
}

// WorkspaceScanSnapshot returns every path the most recently completed
// workspace scan(s) found diagnostics for, merged across linters. The
// caller (GetWorkspaceDiagnostics/Summary in internal/server) is expected
// to skip any path that's also an open buffer, since that path's live,
// possibly-newer diagnostics already supersede a scan result.
func (m *Manager) WorkspaceScanSnapshot() map[string][]lsp.Diagnostic {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	merged := make(map[string][]lsp.Diagnostic)
	for _, byPath := range m.workspaceByCmd {
		for path, diags := range byPath {
			merged[path] = append(merged[path], diags...)
		}
	}
	return merged
}

// WorkspaceScanning reports whether a workspace scan is currently running
// (including one queued to run again immediately after).
func (m *Manager) WorkspaceScanning() bool {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	return m.workspaceRunning
}

// WorkspaceScanError returns the error from cmd's most recently completed
// workspace-scan run, or nil if that run succeeded (or cmd never ran). Same
// rationale as LastError: WorkspaceScanSnapshot alone can't distinguish "no
// issues" from "this linter has been failing" for a given tool.
func (m *Manager) WorkspaceScanError(cmd string) error {
	m.workspaceMu.Lock()
	defer m.workspaceMu.Unlock()
	return m.workspaceErrs[cmd]
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

// maxStderrPreview bounds how much of a failed workspace-scan command's
// stderr gets folded into its returned error — enough to show a real
// culprit (a config error, an unresolvable import, ...) without risking an
// unbounded error string from a linter that dumps something huge.
const maxStderrPreview = 2048

// runWorkspaceLinter executes lc's WorkspaceArgs invocation (a whole-project
// run, e.g. `golangci-lint run ./...`) with workDir as both cwd and the
// base for resolving any relative path the tool reports, and parses its
// output with lc.Format's workspace (multi-file) parser. Mirrors runLinter
// except: no {file}/content substitution (a workspace run has no single
// target file or stdin content — WorkspaceArgs is used verbatim), a longer
// timeout (workspaceScanTimeout, not runLinter's 30s), and a stderr preview
// folded into a run failure's error — a workspace scan's failure is only
// ever visible via WorkspaceScanError (there's no per-file editor context to
// hint at what went wrong the way an open buffer's own diagnostics would),
// so a bare "exit status 1" is much less actionable here than for
// runLinter's single-file case.
func runWorkspaceLinter(parentCtx context.Context, lc config.LinterConfig, workDir string) (map[string][]lsp.Diagnostic, error) {
	parse, ok := workspaceParsers[lc.Format]
	if !ok {
		return nil, fmt.Errorf("lint: no workspace parser for format %q (%s)", lc.Format, lc.Command)
	}

	cmd := expandPath(lc.Command)

	ctx, cancel := context.WithTimeout(parentCtx, workspaceScanTimeout)
	defer cancel()

	proc := exec.CommandContext(ctx, cmd, lc.WorkspaceArgs...)
	procutil.SetPgid(proc)
	proc.Cancel = func() error { return procutil.KillGroup(proc) }
	proc.Dir = workDir
	var out, stderr bytes.Buffer
	proc.Stdout = &out
	proc.Stderr = &stderr
	runErr := proc.Run()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	diags, parseErr := parse(out.Bytes(), workDir)
	if parseErr != nil {
		if runErr != nil {
			return nil, fmt.Errorf("%s: %w%s", lc.Command, runErr, stderrPreview(stderr.Bytes()))
		}
		return nil, parseErr
	}
	return diags, nil
}

// stderrPreview formats up to maxStderrPreview bytes of a failed process's
// stderr as an error-message suffix (": <preview>"), or "" if there was
// none to show.
func stderrPreview(stderr []byte) string {
	stderr = bytes.TrimSpace(stderr)
	if len(stderr) == 0 {
		return ""
	}
	truncated := len(stderr) > maxStderrPreview
	if truncated {
		stderr = stderr[:maxStderrPreview]
	}
	suffix := ""
	if truncated {
		suffix = "…"
	}
	return fmt.Sprintf(": %s%s", stderr, suffix)
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
