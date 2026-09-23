package container

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

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

// bin resolves the runtime, looking beyond PATH for the same reason CLI.bin
// does — an installer that never touched PATH is the common case on macOS.
func (d Docker) bin() string {
	if d.Command != "" {
		return d.Command
	}
	if p, _, err := RuntimePath(); err == nil {
		return p
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
// -i is required: the server reads its stdin, and that is half the wire.
//
// There is deliberately **no "--" separator**. Most CLIs take one; `docker exec`
// does not — it treats "--" as the command to run and fails with
// `exec: "--": executable file not found in $PATH`. An earlier version had one,
// and the unit test asserting its presence encoded the same wrong assumption,
// so both passed until this was run against a real container. Nothing is lost:
// docker stops parsing flags at the container name, and the command here is
// always an absolute path, so there is no leading dash to protect against.
func execArgs(id, user string, env []string, argv []string) []string {
	out := []string{"exec", "-i"}
	if user != "" {
		out = append(out, "-u", user)
	}
	for _, kv := range env {
		out = append(out, "-e", kv)
	}
	out = append(out, id)
	return append(out, argv...)
}

func copyArgs(id, localPath, remotePath string) []string {
	return []string{"cp", localPath, id + ":" + remotePath}
}

// Arch asks the container what it is running on, rather than asking the image
// or the host. A container can be running under emulation, and what matters is
// what its kernel will actually execute.
func (d Docker) Arch(ctx context.Context, id string) (string, error) {
	out, err := d.output(ctx, execArgs(id, d.User, nil, []string{"uname", "-m"})...)
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
	cmd := exec.CommandContext(ctx, d.bin(), execArgs(id, d.User, nil, []string{"/bin/sh", "-c", "[ -x " + shellQuote(path) + " ]"})...)
	cmd.Env = d.env()
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errorsAs(err, &exitErr) {
			return false, nil // a non-zero exit is the answer "no"
		}
		return false, err
	}
	return true, nil
}

// Run executes argv in the container and reports whether it succeeded.
func (d Docker) Run(ctx context.Context, id string, env, argv []string) error {
	_, err := d.output(ctx, execArgs(id, d.User, env, argv)...)
	return err
}

// ProcessCount counts processes in the container whose command line contains
// `contains` and, when `excluding` is non-empty, does not contain it.
//
// Both halves of how this is done are there to stop the probe counting itself,
// which is not hypothetical — two earlier versions did, and reported a server
// running in a container where nothing had ever been started:
//
//   - The patterns travel in **environment variables**, because anything in the
//     command line shows up in `ps`.
//   - The matching is done by **awk reading ENVIRON**, not by grep. Putting the
//     variable in a grep invocation does not help: the shell expands it before
//     exec, so grep's own argv carries the pattern and grep matches itself.
//     awk's argv holds only the program text, which names the variables rather
//     than containing them.
//
// index() is a fixed-string search, so a workspace path containing a regex
// metacharacter cannot quietly change the question.
func (d Docker) ProcessCount(ctx context.Context, id, contains, excluding string) (int, error) {
	const script = `ps -eo args 2>/dev/null | awk '` +
		`index($0, ENVIRON["INDIGO_MATCH"]) && ` +
		`(ENVIRON["INDIGO_EXCLUDE"] == "" || !index($0, ENVIRON["INDIGO_EXCLUDE"])) { n++ } ` +
		`END { print n+0 }'`
	env := []string{"INDIGO_MATCH=" + contains, "INDIGO_EXCLUDE=" + excluding}
	out, err := d.output(ctx, execArgs(id, d.User, env, []string{"/bin/sh", "-c", script})...)
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return 0, fmt.Errorf("unexpected process count %q: %w", strings.TrimSpace(out), convErr)
	}
	return n, nil
}

// env gives a spawned docker its own directory on PATH, so it can find the
// credential helpers that sit beside it. See container.childEnv.
func (d Docker) env() []string {
	path, offPath, err := RuntimePath()
	if err != nil {
		return os.Environ()
	}
	return childEnv(path, offPath)
}

// CopyDirIn copies a directory in. No chmod afterwards: `docker cp` preserves
// modes, and the staging directory already has the binaries executable and the
// manifests not.
//
// Staged and published like CopyIn, for the same reason: Attach skips the copy
// whenever the destination exists, so a half-copied plugin directory at the
// real path would be trusted by every later attach. Here the destination is a
// symlink to the staged copy — see publishDirScript.
func (d Docker) CopyDirIn(ctx context.Context, id, localDir, remoteDir string) error {
	tmp := partialPath(remoteDir)
	if _, err := d.output(ctx, copyArgs(id, localDir, tmp)...); err != nil {
		d.discardPartial(id, tmp)
		return err
	}
	return d.publish(ctx, id, tmp, remoteDir, publishDirScript)
}

// CopyIn copies a file in and makes it executable.
//
// The copy lands at a temporary name and is published to the destination only
// once it is complete and executable. Attach decides whether to copy at all by checking
// that the destination exists and is executable, and `docker cp` creates the
// file with its final mode *before* writing its contents — so copying straight
// to the destination meant an interrupted copy (Ctrl-C, the attach timeout) left
// a truncated binary that every later attach found, trusted and ran, and since
// the path is content-addressed nothing would ever replace it. A second window
// attaching mid-copy could likewise run the file while it was still being
// written. Publishing is a hard link, which is atomic, so the destination is
// either absent or complete.
func (d Docker) CopyIn(ctx context.Context, id, localPath, remotePath string) error {
	tmp := partialPath(remotePath)
	if _, err := d.output(ctx, copyArgs(id, localPath, tmp)...); err != nil {
		d.discardPartial(id, tmp)
		return err
	}
	// docker cp preserves the source mode, but the source may have come from a
	// build directory or an archive that did not; an unexecutable server is a
	// confusing failure two steps later. Done on the temporary name, so what is
	// published is already runnable.
	if _, err := d.output(ctx, execArgs(id, "", nil, []string{"chmod", "+x", tmp})...); err != nil {
		d.discardPartial(id, tmp)
		return err
	}
	return d.publish(ctx, id, tmp, remotePath, publishFileScript)
}

// publish makes a completed copy visible at dst with script (publishFileScript
// or publishDirScript). Run as root, like the copy and chmod before it:
// `docker cp` writes as root regardless of d.User.
func (d Docker) publish(ctx context.Context, id, tmp, dst, script string) error {
	env := []string{"INDIGO_TMP=" + tmp, "INDIGO_DST=" + dst}
	_, err := d.output(ctx, execArgs(id, "", env, []string{"/bin/sh", "-c", script})...)
	if err != nil {
		d.discardPartial(id, tmp)
	}
	return err
}

// discardPartial removes an abandoned temporary copy, best effort. Its own
// short context, because the usual reason to be here is that the caller's has
// expired. Anything it misses is a few megabytes in /tmp under a name nothing
// will ever look for.
func (d Docker) discardPartial(id, tmp string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.output(ctx, execArgs(id, "", []string{"INDIGO_TMP=" + tmp}, []string{"/bin/sh", "-c", `rm -rf -- "$INDIGO_TMP"`})...) //nolint:errcheck
}

// The publish scripts make $INDIGO_TMP visible at $INDIGO_DST without ever
// replacing something already there. Two windows can copy the same
// content-addressed thing at once; the first to publish wins and every later
// one discards its copy and succeeds, since what it wanted is in place.
//
// Both use a primitive that fails atomically when the destination exists, not
// a check followed by mv: `mv src existing-dir` moves src *inside* existing-dir
// rather than failing, and a check-then-mv leaves a window in which two
// publishers both see the path free. The leading [ -e ] is only a fast path.
// Paths travel in the environment to keep quoting out of them.

// publishFileScript hard-links the staged file into place — link(2) refuses an
// existing destination — then drops the staging name.
const publishFileScript = `if [ ! -e "$INDIGO_DST" ] && ! ln -- "$INDIGO_TMP" "$INDIGO_DST" 2>/dev/null && [ ! -e "$INDIGO_DST" ]; then ` +
	`rm -f -- "$INDIGO_TMP"; exit 1; fi; rm -f -- "$INDIGO_TMP"`

// publishDirScript publishes a directory as a relative symlink to the staged
// copy, which stays where it is as the real storage. symlink(2) refuses an
// existing destination, and -n makes ln treat an existing symlink to a
// directory as the file it is instead of creating the link inside it — so a
// losing publisher fails cleanly rather than nesting its copy in the winner's.
// A symlink is followed by everything that reads the directory, including the
// existence check in Attach and the plugin manager's scan.
const publishDirScript = `if [ -e "$INDIGO_DST" ]; then rm -rf -- "$INDIGO_TMP"; exit 0; fi; ` +
	`ln -sn -- "${INDIGO_TMP##*/}" "$INDIGO_DST" 2>/dev/null && exit 0; ` +
	`rm -rf -- "$INDIGO_TMP"; [ -e "$INDIGO_DST" ]`

// partialPath is the staging name a copy is written to before publish makes it
// visible at dst. Beside dst, so a hard link stays on one filesystem and a
// relative symlink resolves; random, so two windows copying at once do not
// write into the same path.
func partialPath(dst string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s.partial-%d", dst, time.Now().UnixNano())
	}
	return dst + ".partial-" + hex.EncodeToString(b[:])
}

// Exec starts argv in the container and returns its stdio as one stream.
//
// **ctx bounds starting the process, not running it.** The returned stream
// outlives this call by design — it is the editor's connection for the whole
// session — so the process is deliberately detached from ctx with
// context.WithoutCancel and owned by procStream.Close instead.
//
// Getting this wrong is not subtle in its effects but is very easy to miss: an
// earlier version passed ctx straight to exec.CommandContext, so the caller's
// ordinary `defer cancel()` killed `docker exec` the moment the function that
// set up the connection returned. The editor connected, completed its
// handshake, and the stream died milliseconds later — and the live tests missed
// it because *their* deferred cancel sat at the end of the test function, where
// it happened to outlive the stream. Production returns immediately; tests do
// not. TestExecSurvivesTheSetupContextBeingCancelled is the regression.
//
// Stderr is deliberately left attached to this process's stderr rather than
// folded into the stream: it is the server's log output and a panic trace, and
// mixing it into the capnp bytes would corrupt the wire and lose the diagnostic
// in the same move.
func (d Docker) Exec(ctx context.Context, id string, argv []string) (io.ReadWriteCloser, error) {
	return d.ExecEnv(ctx, id, nil, argv)
}

// ExecEnv is Exec with extra environment for the started process. The daemon
// the bridge spawns inherits it, which is how INDIGO_PLUGINS_DIR reaches the
// server without the bridge needing to know what it means.
func (d Docker) ExecEnv(ctx context.Context, id string, env, argv []string) (io.ReadWriteCloser, error) {
	procCtx, procCancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := d.execEnvCommand(procCtx, id, env, argv)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		procCancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		procCancel()
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
		procCancel()
		return nil, err
	}
	return &procStream{cmd: cmd, in: stdin, out: stdout, cancel: procCancel}, nil
}

// execEnvCommand builds the `docker exec` for ExecEnv with env's values kept
// off the command line.
//
// env is devcontainer.json's remoteEnv, which can hold secrets (tokens,
// credentials). `-e NAME=value` would put each value in docker's argv, readable
// by any local user through `ps` for as long as the session lasts. `-e NAME`
// alone makes the docker CLI take the value from its own environment instead,
// so the names go on the command line and the values into cmd.Env. Behaviour is
// otherwise identical, empty values included: docker passes a variable that is
// set but empty.
func (d Docker) execEnvCommand(ctx context.Context, id string, env, argv []string) *exec.Cmd {
	names := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		names = append(names, name)
	}
	cmd := exec.CommandContext(ctx, d.bin(), execArgs(id, d.User, names, argv)...)
	// Appended after the base environment: os/exec keeps the last value for a
	// duplicated name, so these win over anything this process inherited.
	cmd.Env = append(d.env(), env...)
	return cmd
}

func (d Docker) Stop(ctx context.Context, id string) error {
	_, err := d.output(ctx, "stop", id)
	return err
}

func (d Docker) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	cmd.Env = d.env()
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
	// cancel releases the detached context the process runs under. Without it
	// that context would live until the process exits on its own, which for an
	// editor server means never.
	cancel context.CancelFunc
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
		if p.cancel != nil {
			p.cancel()
		}
	})
	return p.closeErr
}

// shellQuote wraps s for /bin/sh. Paths here are indigo's own, but quoting is
// cheap and an unquoted path is the kind of thing that works until someone's
// container has a space in it.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
