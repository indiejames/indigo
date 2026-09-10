package format

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/localbin"
	"github.com/indiejames/indigo/internal/lsp"
	"github.com/indiejames/indigo/internal/procutil"
)

// ErrNoFormatter is returned by Format when no formatter is configured or
// auto-detected for the file's extension.
var ErrNoFormatter = errors.New("no formatter available")

// LSPFormatter is the subset of lsp.Manager used for formatting.
type LSPFormatter interface {
	Format(path, content string, opts lsp.FormattingOptions) (string, bool, error)
}

// Manager picks the right formatter for a given file and runs it.
// Priority: user-configured > auto-detected built-in defaults > LSP.
// Dedicated formatters (prettier, gofmt, ...) are preferred over generic LSP
// formatting because they honor project-local config files (.prettierrc,
// EditorConfig, ...) that a language server's own formatter may ignore.
type Manager struct {
	lsp      LSPFormatter
	cfg      *config.Config
	userFmts []config.FormatterConfig
	autoFmts []config.FormatterConfig // subset of DefaultFormatters found in PATH or node_modules
	workDir  string
}

// NewManager builds a Manager. autoFmts are the DefaultFormatters found on
// PATH at startup — a fixed, workspace-independent check. A locally
// installed (node_modules/.bin) formatter is instead resolved lazily, per
// file, in Format below: unlike PATH, which is the same for every file,
// which node_modules/.bin actually applies can differ per file in a
// monorepo with non-hoisted per-package installs, so it can't be decided
// once at startup.
func NewManager(lspMgr *lsp.Manager, cfg *config.Config, workDir string) *Manager {
	m := &Manager{
		lsp:      lspMgr,
		cfg:      cfg,
		userFmts: cfg.Formatters,
		workDir:  workDir,
	}
	for _, d := range config.DefaultFormatters {
		if _, err := exec.LookPath(expandPath(d.Command)); err == nil {
			m.autoFmts = append(m.autoFmts, d)
		}
	}
	return m
}

// localFormatter checks for a DefaultFormatters entry matching ext
// installed under node_modules/.bin somewhere between path's own directory
// and the workspace root (see internal/localbin.Resolve) — the entry
// closest to path wins, so a monorepo package with its own non-hoisted
// node_modules is found even when nothing is installed at the workspace
// root. Only consulted for extensions not already satisfied by a PATH
// match in autoFmts.
func (m *Manager) localFormatter(path, ext string) (config.FormatterConfig, bool) {
	for _, d := range config.DefaultFormatters {
		if !matchesExt(d.Extensions, ext) {
			continue
		}
		cmd := expandPath(d.Command)
		local, ok := localbin.Resolve(filepath.Dir(path), m.workDir, filepath.Base(cmd))
		if !ok {
			continue
		}
		localFC := d
		localFC.Command = local
		return localFC, true
	}
	return config.FormatterConfig{}, false
}

// Format returns the formatted content and whether it changed.
// Returns ErrNoFormatter when no formatter is configured or auto-detected.
//
// Which formatter ran is logged, because the fallback order is invisible from
// the outside and the last step of it silently changes the file's style. A
// project whose formatter is prettier but whose prettier is neither on PATH
// nor in a reachable node_modules/.bin falls through to the language server's
// own formatting, and typescript-language-server's defaults disagree with
// common lint rules — `function () {}` comes back as `function() {}`, which
// then fails the project's own lint. From the user's side that is just "indigo
// mangles my file on save" with nothing anywhere saying why.
func (m *Manager) Format(path, content string) (string, bool, error) {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")

	for _, f := range m.userFmts {
		if matchesExt(f.Extensions, ext) {
			debuglog.Write("format", "%s: configured formatter %q", path, f.Command)
			return runExternal(f, path, content)
		}
	}

	for _, f := range m.autoFmts {
		if matchesExt(f.Extensions, ext) {
			debuglog.Write("format", "%s: formatter %q (found on PATH)", path, f.Command)
			return runExternal(f, path, content)
		}
	}

	if f, ok := m.localFormatter(path, ext); ok {
		debuglog.Write("format", "%s: formatter %q (found in node_modules/.bin)", path, f.Command)
		return runExternal(f, path, content)
	}

	debuglog.Write("format", "%s: no external formatter for .%s found on PATH or in "+
		"node_modules/.bin%s; falling back to the language server's own formatting, whose "+
		"conventions may differ from this project's", path, ext, missingDefaultsNote(ext))
	formatted, changed, err := m.lsp.Format(path, content, m.lspFormattingOptions(ext, content))
	if err != nil {
		return "", false, err
	}
	if changed {
		return formatted, true, nil
	}

	return "", false, ErrNoFormatter
}

// lspFormattingOptions resolves the options to send with an LSP formatting
// request: the tab size / spaces-vs-tabs from the indent style already used in
// content (matches an existing file's convention), falling back to the
// configured per-language default, plus any server-specific settings for the
// extension (EffectiveLSPFormatOptions — VS Code's `typescript.format.*`
// checkboxes and their equivalents for other servers).
func (m *Manager) lspFormattingOptions(ext, content string) lsp.FormattingOptions {
	settings := config.IndentSettings{Style: "tabs", Width: 4}
	if m.cfg != nil {
		settings = m.cfg.EffectiveIndent(ext)
	}
	if detected := config.DetectIndentSettings(content); detected != nil {
		settings = *detected
	}
	width := settings.Width
	if width <= 0 {
		width = 4
	}
	return lsp.FormattingOptions{
		TabSize:      width,
		InsertSpaces: settings.Style == "spaces",
		Extra:        m.cfg.EffectiveLSPFormatOptions(ext),
	}
}

// externalFormatTimeout bounds how long an external formatter process may
// run before it's killed. Overridable in tests so a hang-past-timeout
// regression test doesn't have to wait out the real production duration.
var externalFormatTimeout = 10 * time.Second

func runExternal(fc config.FormatterConfig, filePath, content string) (string, bool, error) {
	cmd := expandPath(fc.Command)
	args := expandArgs(fc.Args, filePath)

	ctx, cancel := context.WithTimeout(context.Background(), externalFormatTimeout)
	defer cancel()

	proc := exec.CommandContext(ctx, cmd, args...)
	procutil.SetPgid(proc)
	proc.Cancel = func() error { return procutil.KillGroup(proc) }
	// A formatter resolved out of a package's own node_modules runs with that
	// package as its cwd. findFormatter picks the binary by walking up from the
	// file, so running it from the workspace root would assert two different
	// things about which package the file belongs to — and these tools read the
	// cwd, not their own location, when they look for config (prettier resolves
	// .prettierrc from the cwd upward). Left unset otherwise, inheriting the
	// server's cwd, which is the workspace root: unchanged behaviour for
	// anything found on PATH.
	if dir, ok := localbin.PackageDir(cmd); ok {
		proc.Dir = dir
	}
	proc.Stdin = strings.NewReader(content)
	var out, errBuf bytes.Buffer
	proc.Stdout = &out
	proc.Stderr = &errBuf

	if err := proc.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return content, false, fmt.Errorf("%s: %s", fc.Command, msg)
	}

	result := out.String()
	if strings.TrimSpace(result) == "" && strings.TrimSpace(content) != "" {
		return content, false, fmt.Errorf("%s: produced empty output for non-empty input, refusing to apply", fc.Command)
	}
	return result, result != content, nil
}

// missingDefaultsNote names the formatter indigo would have used for ext had
// it been installed, for the fallback log line above. Knowing the answer is
// "prettier, and it isn't installed" is the difference between a mystifying
// reformat and a one-line fix.
func missingDefaultsNote(ext string) string {
	var names []string
	for _, d := range config.DefaultFormatters {
		if matchesExt(d.Extensions, ext) {
			names = append(names, d.Command)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return " (expected one of: " + strings.Join(names, ", ") + ")"
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
