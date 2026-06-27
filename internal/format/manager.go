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
	"github.com/indiejames/indigo/internal/lsp"
)

// ErrNoFormatter is returned by Format when no formatter is configured or
// auto-detected for the file's extension.
var ErrNoFormatter = errors.New("no formatter available")

// LSPFormatter is the subset of lsp.Manager used for formatting.
type LSPFormatter interface {
	Format(path, content string) (string, bool, error)
}

// Manager picks the right formatter for a given file and runs it.
// Priority: user-configured > LSP > auto-detected built-in defaults.
type Manager struct {
	lsp      LSPFormatter
	userFmts []config.FormatterConfig
	autoFmts []config.FormatterConfig // subset of DefaultFormatters found in PATH or node_modules
	workDir  string
}

// NewManager builds a Manager. autoFmts are detected once at startup by
// scanning PATH and <workDir>/node_modules/.bin/.
func NewManager(lspMgr *lsp.Manager, cfg *config.Config, workDir string) *Manager {
	m := &Manager{
		lsp:      lspMgr,
		userFmts: cfg.Formatters,
		workDir:  workDir,
	}
	localBin := filepath.Join(workDir, "node_modules", ".bin")
	for _, d := range config.DefaultFormatters {
		cmd := expandPath(d.Command)
		if _, err := exec.LookPath(cmd); err == nil {
			m.autoFmts = append(m.autoFmts, d)
			continue
		}
		// Also check <workDir>/node_modules/.bin/<command> for locally-installed tools.
		local := filepath.Join(localBin, filepath.Base(cmd))
		if _, err := os.Stat(local); err == nil {
			localFC := d
			localFC.Command = local
			m.autoFmts = append(m.autoFmts, localFC)
		}
	}
	return m
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

	if formatted, changed, err := m.lsp.Format(path, content); err == nil && changed {
		return formatted, true, nil
	}

	for _, f := range m.autoFmts {
		if matchesExt(f.Extensions, ext) {
			return runExternal(f, path, content)
		}
	}

	return "", false, ErrNoFormatter
}

func runExternal(fc config.FormatterConfig, filePath, content string) (string, bool, error) {
	cmd := expandPath(fc.Command)
	args := expandArgs(fc.Args, filePath)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc := exec.CommandContext(ctx, cmd, args...)
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
