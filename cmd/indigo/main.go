package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: indigo <file>")
		os.Exit(1)
	}
	filePath := os.Args[1]

	// Resolve absolute path so server can identify the working directory.
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		fatalf("resolve path: %v", err)
	}
	workDir := filepath.Dir(absPath)
	sockPath := server.SocketPath(workDir)

	if !server.IsRunning(sockPath) {
		startServer(workDir)
	}

	// Give the server a moment to start if we just spawned it.
	if err := waitForServer(sockPath, 3*time.Second); err != nil {
		fatalf("server did not start: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}

	rpc, err := client.Dial(sockPath)
	if err != nil {
		fatalf("connect to server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	bufID, content, version, err := rpc.OpenFile(ctx, absPath)
	cancel()
	if err != nil {
		fatalf("open file: %v", err)
	}

	m := client.New(rpc, bufID, content, version, absPath, cfg)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fatalf("run: %v", err)
	}
}

func startServer(workDir string) {
	// Fork a background server process.
	// We re-exec ourselves with the hidden --server flag.
	exe, err := os.Executable()
	if err != nil {
		fatalf("locate executable: %v", err)
	}

	proc, err := os.StartProcess(exe, []string{exe, "--server", workDir}, &os.ProcAttr{
		Dir:   workDir,
		Files: []*os.File{nil, nil, nil}, // detach stdio
	})
	if err != nil {
		fatalf("start server: %v", err)
	}
	proc.Release() // don't wait — server runs independently
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

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "indigo: "+format+"\n", args...)
	os.Exit(1)
}

func init() {
	// If invoked as `indigo --server <dir>`, run in server mode.
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
