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
func (m *Manager) Format(path, content string) (string, bool, error) {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")

	for _, f := range m.userFmts {
		if matchesExt(f.Extensions, ext) {
			return runExternal(f, path, content)
		}
	}

	for _, f := range m.autoFmts {
		if matchesExt(f.Extensions, ext) {
			return runExternal(f, path, content)
		}
	}

	if f, ok := m.localFormatter(path, ext); ok {
		return runExternal(f, path, content)
	}

	formatted, changed, err := m.lsp.Format(path, content, m.lspFormattingOptions(ext, content))
	if err != nil {
		return "", false, err
	}
	if changed {
		return formatted, true, nil
	}

	return "", false, ErrNoFormatter
}

// lspFormattingOptions resolves the tab size / spaces-vs-tabs to send with an
// LSP formatting request: the indent style already used in content takes
// precedence (matches an existing file's convention), falling back to the
// configured per-language default.
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
	return lsp.FormattingOptions{TabSize: width, InsertSpaces: settings.Style == "spaces"}
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
