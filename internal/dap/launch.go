package dap

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/indiejames/indigo/internal/procutil"
)

// Adapters reach their client in one of two ways, and this package supports
// both behind the same Client:
//
//   - stdio: the client spawns the adapter and speaks DAP over its stdin and
//     stdout (debugpy, codelldb).
//   - listen-then-dial: the adapter starts a server and prints the address it
//     listens on; the client dials it (`dlv dap --listen=127.0.0.1:0`).
//
// Either way the adapter process is started in its own process group and the
// whole group is killed when the Client closes: adapters start the debuggee as
// a child, and killing only the adapter would orphan the program being
// debugged — the lesson internal/procutil exists for.

// StartStdio spawns an adapter that speaks DAP on its stdio. Its stderr goes to
// stderr, which is the process's log.
func StartStdio(name string, args []string, dir string, env []string, handler func(Event)) (*Client, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stderr = os.Stderr
	procutil.SetPgid(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	c := NewClient(&stdioConn{r: stdout, w: stdin}, handler)
	c.onEnd = func() {
		procutil.KillGroup(cmd) //nolint:errcheck
		cmd.Wait()              //nolint:errcheck
	}
	return c, nil
}

// listenAddr finds the address an adapter says it listens on, e.g. Delve's
// "DAP server listening at: 127.0.0.1:54321".
var listenAddr = regexp.MustCompile(`listening at:?\s*(\S+:\d+)`)

// StartListening spawns an adapter that serves DAP on a TCP address it prints
// to stdout, waits for that line, and dials it. ctx bounds only the startup.
func StartListening(ctx context.Context, name string, args []string, dir string, env []string, handler func(Event)) (*Client, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stderr = os.Stderr
	procutil.SetPgid(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	kill := func() {
		procutil.KillGroup(cmd) //nolint:errcheck
		cmd.Wait()              //nolint:errcheck
	}

	addrCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		found := false
		for sc.Scan() {
			if !found {
				if m := listenAddr.FindStringSubmatch(sc.Text()); m != nil {
					addrCh <- m[1]
					found = true
				}
			}
			// Keep draining after the address: the adapter goes on writing
			// here (Delve logs, and a debuggee's output can land here too),
			// and a full pipe would block it.
		}
		if !found {
			close(addrCh)
		}
		// Scan stops early on a line longer than its buffer; the rest still
		// has to be read, for the same reason.
		io.Copy(io.Discard, stdout) //nolint:errcheck
	}()

	var addr string
	select {
	case a, ok := <-addrCh:
		if !ok {
			kill()
			return nil, fmt.Errorf("%s exited without reporting a listen address", name)
		}
		addr = a
	case <-ctx.Done():
		kill()
		return nil, fmt.Errorf("%s did not report a listen address: %w", name, ctx.Err())
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		kill()
		return nil, fmt.Errorf("dial %s at %s: %w", name, addr, err)
	}
	c := NewClient(conn, handler)
	c.onEnd = kill
	c.addr = addr
	return c, nil
}

// Dial connects to an adapter already listening at addr — a second session on
// the adapter a StartListening client started, which is how an adapter's
// startDebugging request is answered. Closing it closes only the connection.
func Dial(ctx context.Context, addr string, handler func(Event)) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial the debugger at %s: %w", addr, err)
	}
	c := NewClient(conn, handler)
	c.addr = addr
	return c, nil
}

// stdioConn joins a child's stdout and stdin into one stream. Closing closes
// stdin, which most adapters take as the end of the session.
type stdioConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (s *stdioConn) Read(b []byte) (int, error)  { return s.r.Read(b) }
func (s *stdioConn) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *stdioConn) Close() error {
	err := s.w.Close()
	s.r.Close() //nolint:errcheck
	return err
}

// shutdownGrace bounds a polite Disconnect before the process is killed.
const shutdownGrace = 3 * time.Second

// Shutdown asks the adapter to end the session (terminating the debuggee) and
// then closes the connection and process regardless of the answer — a wedged
// adapter must not keep a debuggee running.
func (c *Client) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	c.Disconnect(ctx, true) //nolint:errcheck
	c.Close()               //nolint:errcheck
}
