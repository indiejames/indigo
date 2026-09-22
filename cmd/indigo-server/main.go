// Command indigo-server is the editor server for running inside a dev
// container. It has two modes, and the distinction is the point.
//
//	indigo-server <workspace>            bridge: relay this process's stdio to
//	                                     the workspace's server, starting it if
//	                                     it is not already running
//	indigo-server --daemon <workspace>   the server itself, on a unix socket
//	                                     inside the container
//	indigo-server --mcp                  MCP over stdio for an agent running in
//	                                     the container, against the same server
//
// # Why a bridge rather than serving stdio directly
//
// An earlier version served the connection straight over stdin/stdout, which
// worked and was wrong in a way that only shows up with a second window: one
// `docker exec` is one process, so every window got its *own* server, with its
// own buffer table. Two windows editing one file would each hold a private copy
// and write over each other, with none of the operational transform that makes
// concurrent editing converge — measured, two attaches produced two servers.
//
// On the host that never arises: one server per workspace listens on a unix
// socket and every window connects to it. The fix is to keep exactly that
// arrangement inside the container and make `docker exec` a *transport* rather
// than the server. The daemon is the same server, unchanged; this process is a
// pipe between the host's stdio and its socket.
//
// # Why this binary exists at all
//
// It must cross-compile, and cmd/indigo cannot: internal/highlight pulls in a
// cgo tree-sitter binding whatever the build tags say. The server never needed
// it — syntax highlighting is entirely client-side — so this builds static, for
// any Linux the container happens to be, and the host copies it in rather than
// asking anyone to modify their image.
//
// Nothing may write to stdout in bridge mode: stdout is the wire.
package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/indiejames/indigo/internal/agenttools"
	"github.com/indiejames/indigo/internal/debuglog"
	"github.com/indiejames/indigo/internal/hangdetect"
	"github.com/indiejames/indigo/internal/server"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "--daemon" {
		runDaemon(args[1])
		return
	}
	// --mcp is `indigo --mcp` for inside a dev container, where there is no
	// `indigo` — only this binary. Register it from the container with
	//
	//	claude mcp add --scope user indigo -- /tmp/.indigo-server --mcp
	//
	// (/tmp/.indigo-server is the stable link Attach keeps pointing at the
	// current build; see container.StableServerLink.) The workspace is found
	// from the cwd exactly as `indigo --mcp` finds it, so an agent started in
	// the workspace reaches the daemon the editor's bridge started. When none
	// is running, it starts one the same way a bridge would — the --daemon
	// mode below, not `indigo --server` — so a window attaching later joins it
	// instead of starting a second server.
	if len(args) == 1 && args[0] == "--mcp" {
		agenttools.RunStandaloneWith(startDaemon)
		return
	}
	// --client <token> marks this bridge in the container's process list so a
	// window can tell its own from another's when deciding whether it is the
	// last one out (see container.OtherWindowsAttached). Nothing reads it here;
	// being *visible* is the whole job.
	if len(args) == 3 && args[1] == "--client" {
		args = args[:1]
	}
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: %s [--daemon] <workspace-dir> [--client <token>]\n       %s --mcp\n", os.Args[0], os.Args[0])
		os.Exit(2)
	}
	runBridge(args[0])
}

// runDaemon is the server proper: one per workspace, on a unix socket, exiting
// when its last client disconnects — exactly as on the host.
func runDaemon(dir string) {
	hangdetect.Start()
	defer hangdetect.Stop()

	srv, err := server.New(dir)
	if errors.Is(err, server.ErrAlreadyRunning) {
		// Another bridge's daemon won the startup race; our bridge dials it.
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-server: %v\n", err)
		os.Exit(1)
	}
	srv.Wait()
}

// runBridge connects this process's stdio to the workspace's server, starting
// one if it is not already running, and copies bytes until either end closes.
func runBridge(dir string) {
	sockPath := server.SocketPath(dir)
	if !server.IsRunning(sockPath) {
		if err := startDaemon(dir); err != nil {
			fmt.Fprintf(os.Stderr, "indigo-server: %v\n", err)
			os.Exit(1)
		}
	}
	conn, err := dialWithRetry(sockPath, daemonStartTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "indigo-server: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close() //nolint:errcheck

	// Copy both ways and stop as soon as either direction ends. Whichever
	// finishes first, the other is no longer useful: the client has gone, or
	// the server has.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(conn, os.Stdin) //nolint:errcheck
		// Half-close so the server sees its client leave, rather than a
		// connection that merely went quiet.
		if unixConn, ok := conn.(*net.UnixConn); ok {
			unixConn.CloseWrite() //nolint:errcheck
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(os.Stdout, conn) //nolint:errcheck
		done <- struct{}{}
	}()
	<-done
}

// daemonStartTimeout bounds waiting for a just-started server's socket. The
// host uses 3s for the same wait; a container can be slower to schedule a new
// process, especially while an image's lifecycle hooks are still settling.
const daemonStartTimeout = 10 * time.Second

// startDaemon spawns the server and detaches it completely.
//
// Setsid is load-bearing, not tidiness. The host kills this bridge by process
// *group* when a window closes — that is what stops a killed server orphaning
// its plugins — so a daemon left in the bridge's group would be killed along
// with the first window to close, taking every other window's server with it.
// A new session puts it out of reach.
func startDaemon(dir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	// The daemon keeps this descriptor as its stderr for its whole life, so its
	// output stays in the day's file it was started under (see debuglog).
	logFile, _ := debuglog.Open()
	proc, err := os.StartProcess(exe, []string{exe, "--daemon", dir}, &os.ProcAttr{
		Dir:   dir,
		Files: []*os.File{nil, nil, logFile},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if logFile != nil {
		logFile.Close() //nolint:errcheck
	}
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	return proc.Release()
}

// dialWithRetry waits for the socket to accept a connection.
//
// Polling rather than one attempt because two windows can attach at the same
// instant: both find no server, both try to start one, and the loser's spawn
// fails or exits while the winner's socket is still appearing. Retrying makes
// that race resolve itself instead of failing the second window.
func dialWithRetry(sockPath string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	return nil, fmt.Errorf("connect to server at %s: %w", sockPath, lastErr)
}
