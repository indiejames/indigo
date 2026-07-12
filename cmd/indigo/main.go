package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/app"
	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/highlight"
	"github.com/indiejames/indigo/internal/server"
	"github.com/indiejames/indigo/internal/theme"
)

func main() {
	// Handle --import-theme helix:<path> or --import-theme vscode:<path>.
	if len(os.Args) == 3 && os.Args[1] == "--import-theme" {
		cfgDir, _ := config.ConfigDir()
		outDir := filepath.Join(cfgDir, "indigo", "themes")
		outPath, err := theme.Import(os.Args[2], outDir)
		if err != nil {
			fatalf("import theme: %v", err)
		}
		fmt.Printf("Theme written to %s\n", outPath)
		fmt.Printf("To activate it, add the following to ~/.config/indigo/config.toml:\n")
		fmt.Printf("  theme = %q\n", strings.TrimSuffix(filepath.Base(outPath), ".toml"))
		return
	}

	// Parse optional +N line argument (e.g. indigo +42 foo.go).
	startLine := 0
	args := os.Args[1:]
	if len(args) >= 2 && strings.HasPrefix(args[0], "+") {
		if n, err := strconv.Atoi(args[0][1:]); err == nil && n > 0 {
			startLine = n - 1 // convert to 0-based
			args = args[1:]
		}
	}

	// No file argument → open an untitled empty buffer.
	if len(args) == 0 {
		openUntitled(startLine)
		return
	}
	target := args[0]

	absTarget, err := filepath.Abs(target)
	if err != nil {
		fatalf("resolve path: %v", err)
	}

	info, err := os.Stat(absTarget)
	if err != nil {
		if !os.IsNotExist(err) {
			fatalf("stat %s: %v", absTarget, err)
		}
		// File doesn't exist — create it along with any missing parent directories.
		if mkErr := os.MkdirAll(filepath.Dir(absTarget), 0o755); mkErr != nil {
			fatalf("create directory for %s: %v", absTarget, mkErr)
		}
		if mkErr := os.WriteFile(absTarget, nil, 0o644); mkErr != nil {
			fatalf("create %s: %v", absTarget, mkErr)
		}
		if info, err = os.Stat(absTarget); err != nil {
			fatalf("stat %s: %v", absTarget, err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}
	loadAndApplyTheme(cfg)

	// Determine workspace root.
	var workDir string
	if info.IsDir() {
		workDir = absTarget
	} else {
		workDir = gitRoot(absTarget)
		if workDir == "" {
			workDir = filepath.Dir(absTarget)
		}
	}

	sockPath := server.SocketPath(workDir)
	if !server.IsRunning(sockPath) {
		startServer(workDir)
	}
	if err := waitForServer(sockPath, 3*time.Second); err != nil {
		fatalf("server did not start: %v", err)
	}

	rpc, err := client.Dial(sockPath)
	if err != nil {
		fatalf("connect to server: %v", err)
	}

	var a *app.App
	if info.IsDir() {
		// Start with the file picker open.
		a = app.NewWithPicker(rpc, cfg, absTarget)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		bufID, content, version, fromRecovery, err := rpc.OpenFile(ctx, absTarget)
		cancel()
		if err != nil {
			fatalf("open file: %v", err)
		}
		a = app.New(rpc, bufID, content, version, absTarget, cfg, fromRecovery, workDir, startLine)
	}

	p := tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion())
	rpc.SetPushSender(p.Send)
	if _, err := p.Run(); err != nil {
		fatalf("run: %v", err)
	}
}

func startServer(workDir string) {
	exe, err := os.Executable()
	if err != nil {
		fatalf("locate executable: %v", err)
	}
	logPath := filepath.Join(os.TempDir(), "indigo-plugins.log")
	logFile, _ := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	proc, err := os.StartProcess(exe, []string{exe, "--server", workDir}, &os.ProcAttr{
		Dir:   workDir,
		Files: []*os.File{nil, nil, logFile},
	})
	if logFile != nil {
		logFile.Close() //nolint:errcheck
	}
	if err != nil {
		fatalf("start server: %v", err)
	}
	proc.Release() //nolint:errcheck
}

func waitForServer(sockPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if server.IsRunning(sockPath) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", sockPath)
}

func gitRoot(path string) string {
	dir := path
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func openUntitled(startLine int) {
	workDir, err := os.Getwd()
	if err != nil {
		fatalf("getwd: %v", err)
	}
	// Walk up from cwd to find a git root so the server covers the workspace.
	if root := gitRoot(workDir); root != "" {
		workDir = root
	}

	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}
	loadAndApplyTheme(cfg)

	sockPath := server.SocketPath(workDir)
	if !server.IsRunning(sockPath) {
		startServer(workDir)
	}
	if err := waitForServer(sockPath, 3*time.Second); err != nil {
		fatalf("server did not start: %v", err)
	}

	rpc, err := client.Dial(sockPath)
	if err != nil {
		fatalf("connect to server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	bufID, content, version, _, err := rpc.OpenFile(ctx, "")
	cancel()
	if err != nil {
		fatalf("open untitled buffer: %v", err)
	}

	a := app.New(rpc, bufID, content, version, "", cfg, false, workDir, startLine)
	p := tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion())
	rpc.SetPushSender(p.Send)
	if _, err := p.Run(); err != nil {
		fatalf("run: %v", err)
	}
}

// loadAndApplyTheme resolves the theme named in cfg, applies it to the client
// UI styles, and updates the syntax highlight palette. Errors are non-fatal;
// the built-in default-dark theme is used as a fallback.
func loadAndApplyTheme(cfg *config.Config) {
	cfgDir, _ := config.ConfigDir()
	t, err := theme.Load(cfg.Theme, cfgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo: theme %q: %v (using default)\n", cfg.Theme, err)
		t = theme.Default()
	}
	client.ApplyTheme(t)

	// Convert theme.SyntaxStyle → highlight.SyntaxStyle (same fields, separate types).
	hl := make(map[string]highlight.SyntaxStyle, len(t.Syntax))
	for scope, s := range t.Syntax {
		hl[scope] = highlight.SyntaxStyle{Fg: s.Fg, Bold: s.Bold, Italic: s.Italic}
	}
	highlight.ApplyTheme(hl)

	// Register user-defined file type aliases (e.g. ".myext" → "go").
	for from, to := range cfg.FileTypes {
		highlight.RegisterAlias(from, to)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "indigo: "+format+"\n", args...)
	os.Exit(1)
}

func init() {
	if len(os.Args) == 3 && os.Args[1] == "--server" {
		runServer(os.Args[2])
		os.Exit(0)
	}
}

func runServer(dir string) {
	srv, err := server.New(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
	srv.Wait()
}
