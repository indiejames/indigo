package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/indiejames/indigo/internal/procutil"
)

// Docker drives a docker-compatible CLI. Command is "docker" unless something
// else is configured; podman answers the same subcommands.
type Docker struct {
	Command string
	// User is the container user to run as — devcontainer.json's remoteUser.
	// Empty means the image's default. It matters beyond tidiness: a server
	// running as root writes files the developer then cannot edit from
	// anywhere else.
	User string
}

// errorsAs is errors.As, named locally so the import list stays short in a
// file that is mostly os/exec plumbing.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// stderrSink is where a container-side server's stderr goes: this process's
// stderr, so its log lines and any panic reach the same terminal as the rest of
// indigo's diagnostics.
func stderrSink() io.Writer { return os.Stderr }

func (d Docker) bin() string {
	if d.Command != "" {
		return d.Command
	}
	return "docker"
}

// execArgs builds the argv for running a command inside a container.
//
// Factored out so it can be asserted on: everything else in this file needs a
// real container to exercise, but whether -t appears is exactly the thing that
// must never change and would be invisible if it did — a TTY translates \n to
// \r\n and silently corrupts the capnp stream rather than failing.
//
// -i is required (the server reads its stdin), and "--" stops the CLI from
// interpreting a leading dash in the command as its own flag.
func execArgs(id, user string, argv []string) []string {
	out := []string{"exec", "-i"}
	if user != "" {
		out = append(out, "-u", user)
	}
	out = append(out, id, "--")
	return append(out, argv...)
}

func copyArgs(id, localPath, remotePath string) []string {
	return []string{"cp", localPath, id + ":" + remotePath}
}

// Arch asks the container what it is running on, rather than asking the image
// or the host. A container can be running under emulation, and what matters is
// what its kernel will actually execute.
func (d Docker) Arch(ctx context.Context, id string) (string, error) {
	out, err := d.output(ctx, execArgs(id, d.User, []string{"uname", "-m"})...)
	if err != nil {
		return "", err
	}
	switch machine := strings.TrimSpace(out); machine {
	case "aarch64", "arm64":
		return "arm64", nil
	case "x86_64", "amd64":
		return "amd64", nil
	default:
		return "", fmt.Errorf("unsupported container architecture %q", machine)
	}
}

// FileExists uses the shell's own test rather than a stat binary: a minimal
// image may have neither stat nor test as a separate executable, but any
// container indigo can run a server in has some /bin/sh.
func (d Docker) FileExists(ctx context.Context, id, path string) (bool, error) {
	cmd := exec.CommandContext(ctx, d.bin(), execArgs(id, d.User, []string{"/bin/sh", "-c", "[ -x " + shellQuote(path) + " ]"})...)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errorsAs(err, &exitErr) {
			return false, nil // a non-zero exit is the answer "no"
		}
		return false, err
	}
	return true, nil
}

func (d Docker) CopyIn(ctx context.Context, id, localPath, remotePath string) error {
	if _, err := d.output(ctx, copyArgs(id, localPath, remotePath)...); err != nil {
		return err
	}
	// docker cp preserves the source mode, but the source may have come from a
	// build directory or an archive that did not; an unexecutable server is a
	// confusing failure two steps later.
	_, err := d.output(ctx, execArgs(id, "", []string{"chmod", "+x", remotePath})...)
	return err
}

// Exec starts argv in the container and returns its stdio as one stream.
//
// Stderr is deliberately left attached to this process's stderr rather than
// folded into the stream: it is the server's log output and a panic trace, and
// mixing it into the capnp bytes would corrupt the wire and lose the diagnostic
// in the same move.
func (d Docker) Exec(ctx context.Context, id string, argv []string) (io.ReadWriteCloser, error) {
	cmd := exec.CommandContext(ctx, d.bin(), execArgs(id, d.User, argv)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderrSink()
	// Its own process group, so Close can take the whole tree down. The server
	// spawns plugin processes of its own, and killing only the direct child
	// orphans them — inside a container they would linger holding the exec's
	// stdio, which is the same lesson internal/procutil was written for after
	// linters and formatters did exactly this.
	procutil.SetPgid(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &procStream{cmd: cmd, in: stdin, out: stdout}, nil
}

func (d Docker) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s %s: %w: %s", d.bin(), strings.Join(args, " "), err, msg)
		}
		return "", fmt.Errorf("%s %s: %w", d.bin(), strings.Join(args, " "), err)
	}
	return stdout.String(), nil
}

// procStream is a child process's stdin and stdout as one stream. Close ends
// the process rather than only closing the pipes, so a client that hangs up
// does not leave a server running in the container.
type procStream struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out io.ReadCloser
	// once guards the teardown, because Close has two callers that do not know
	// about each other: whoever opened the stream, and capnp, which owns it
	// once client.DialStream has been handed it and closes it when the
	// connection tears down. os/exec's Wait is not safe to call twice — a
	// second concurrent call blocks forever on the first's completion — so
	// without this a perfectly ordinary shutdown deadlocks one of the two.
	once     sync.Once
	closeErr error
}

func (p *procStream) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *procStream) Write(b []byte) (int, error) { return p.in.Write(b) }

func (p *procStream) Close() error {
	p.once.Do(func() {
		// Closing stdin first gives the server the chance to shut down
		// cleanly — ServeStream treats the stream closing as its last client
		// leaving — before the process is killed out from under it.
		p.closeErr = p.in.Close()
		if p.cmd.Process != nil {
			// The group, not just the process: see SetPgid at the start.
			procutil.KillGroup(p.cmd) //nolint:errcheck
		}
		p.cmd.Wait()  //nolint:errcheck
		p.out.Close() //nolint:errcheck
	})
	return p.closeErr
}

// shellQuote wraps s for /bin/sh. Paths here are indigo's own, but quoting is
// cheap and an unquoted path is the kind of thing that works until someone's
// container has a space in it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
