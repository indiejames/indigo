// Package container attaches indigo to a running container: it puts a server
// binary inside, starts it, and hands back the stream the client talks capnp
// over.
//
// # Why a seam
//
// Nothing here can be verified on a machine without a container runtime, and
// the development machine for this work had none. So the orchestration — which
// architecture, whether the binary is already there, what argv to run, in what
// order — is written against an interface and tested against a fake, and the
// part that genuinely needs docker is reduced to building an argv and running
// it. What cannot be tested is kept small enough to read.
//
// # The hazard that is not obvious
//
// The exec must not allocate a TTY. A pty translates \n to \r\n on output, and
// capnp is a binary framing — so a TTY does not fail, it silently corrupts
// every message that happens to contain a byte matching a newline. `docker exec`
// allocates one only with -t, so the rule is simply never to pass it, and
// TestExecArgsNeverAllocateATTY exists because that is a one-character mistake
// with a symptom nobody would trace back to it.
package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Runtime is a container engine — docker, or anything that behaves like it.
type Runtime interface {
	// Arch reports the container's machine architecture as a Go GOARCH value
	// ("arm64", "amd64"), so the caller can pick a matching binary.
	Arch(ctx context.Context, id string) (string, error)
	// FileExists reports whether path exists inside the container.
	FileExists(ctx context.Context, id, path string) (bool, error)
	// CopyIn copies localPath into the container at remotePath and makes it
	// executable.
	CopyIn(ctx context.Context, id, localPath, remotePath string) error
	// ProcessCount counts processes in the container whose command line
	// contains `contains` and does not contain `excluding` (ignored when
	// empty).
	ProcessCount(ctx context.Context, id, contains, excluding string) (int, error)
	// Exec starts argv inside the container with no TTY, and returns its
	// stdin/stdout as one stream. Closing the stream ends the process.
	Exec(ctx context.Context, id string, argv []string) (io.ReadWriteCloser, error)
}

// ServerPath is where a server binary is placed inside the container.
//
// /tmp rather than the user's home: a dev container's remoteUser may have no
// home directory, and /tmp is writable in every image worth attaching to.
//
// The name carries the binary's **content hash**, which makes the placement
// content-addressed: a different build lands at a different path, so upgrading
// indigo can never leave a container running the previous server. An earlier
// version stamped only the architecture and reasoned that "containers rarely
// outlive an upgrade" — they do so constantly during development, and the
// failure is not subtle. A container started before a new flag existed met a
// client that passed it, and the server answered with a usage message instead
// of speaking capnp.
//
// Old binaries are left behind rather than cleaned up. They are a few megabytes
// in a container's /tmp, which goes away with the container, and deleting a
// path another window may be running from is a worse trade.
func ServerPath(goarch, contentHash string) string {
	return "/tmp/.indigo-server-" + goarch + "-" + contentHash
}

// hashFile returns a short content hash of the file at path.
//
// Short because it names a file a human may see in a process list, and eight
// bytes is ample to distinguish builds of one program — this is addressing, not
// a security boundary.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)[:8]), nil
}

// AttachOptions carries the parts of an attach that vary.
type AttachOptions struct {
	// ClientToken marks this window's bridge process inside the container, so
	// the windows can be told apart in a process list. Without it a window
	// leaving cannot distinguish its own bridge from another window's — see
	// OtherWindowsAttached.
	ClientToken string
	// ServerPath overrides where the server lives inside the container. Empty
	// means the architecture-stamped default. A project sets it through
	// customizations.indigo.serverPath when its image already ships one, which
	// skips the copy entirely.
	ServerPath string
	// Locate finds a local binary to copy in, consulted only when the
	// container does not already have one.
	Locate func(goarch string) (string, error)
}

// Attach makes a server available inside the container and starts it for
// workspaceDir, returning the stream to hand to client.DialStream.
//
// The binary is copied only when it is not already there, so the second window
// onto a container pays nothing but a stat. Which binary "there" means is
// decided by content (see ServerPath), so the saving never comes at the cost of
// running a stale one.
func Attach(ctx context.Context, rt Runtime, id, workspaceDir string, opts AttachOptions) (io.ReadWriteCloser, error) {
	goarch, err := rt.Arch(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("determine container architecture: %w", err)
	}

	remote := opts.ServerPath
	var local string
	if remote == "" {
		// The local binary is located *first*, because its content decides
		// where it goes. That costs a hash of a few megabytes on every attach
		// and buys the guarantee that a container can never be left running a
		// server from a previous build — which is worth far more than the
		// milliseconds, and was the bug that made this content-addressed.
		if opts.Locate == nil {
			return nil, fmt.Errorf("no server path given and no way to find one")
		}
		local, err = opts.Locate(goarch)
		if err != nil {
			return nil, err
		}
		hash, err := hashFile(local)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", local, err)
		}
		remote = ServerPath(goarch, hash)
	}

	present, err := rt.FileExists(ctx, id, remote)
	if err != nil {
		return nil, fmt.Errorf("check for %s in container: %w", remote, err)
	}
	if !present {
		if local == "" {
			return nil, fmt.Errorf("no server at %s in the container and no way to supply one", remote)
		}
		if err := rt.CopyIn(ctx, id, local, remote); err != nil {
			return nil, fmt.Errorf("copy server into container: %w", err)
		}
	}

	argv := []string{remote, workspaceDir}
	if opts.ClientToken != "" {
		argv = append(argv, "--client", opts.ClientToken)
	}
	stream, err := rt.Exec(ctx, id, argv)
	if err != nil {
		return nil, fmt.Errorf("start server in container: %w", err)
	}
	return stream, nil
}

// LocateServerBinary finds the statically-linked Linux server built by
// `make build-container-server`.
//
// Searched rather than embedded: embedding both architectures would add ~11 MB
// to every client, and a //go:embed of a file produced by a separate make
// target would break a plain `go build` whenever it had not been run. The
// error names the target, because "not found" without it is a dead end.
func LocateServerBinary(goarch string) (string, error) {
	name := "indigo-server-linux-" + goarch
	var candidates []string
	if override := os.Getenv("INDIGO_CONTAINER_SERVER"); override != "" {
		candidates = append(candidates, override)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, name),
			filepath.Join(dir, "dist", name),
			filepath.Join(dir, "..", "dist", name),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".indigo", name))
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("no %s found (looked in %s) — build one with `make build-container-server`",
		name, strings.Join(candidates, ", "))
}

// ServerRunning reports whether a server daemon for workspaceDir is still
// running inside the container.
func ServerRunning(ctx context.Context, rt Runtime, id, workspaceDir string) (bool, error) {
	n, err := rt.ProcessCount(ctx, id, "--daemon "+workspaceDir, "")
	return n > 0, err
}

// OtherWindowsAttached counts the windows attached to workspaceDir other than
// the one identified by myToken.
//
// This is what makes quitting fast. The obvious question — "is the daemon still
// running?" — cannot be answered usefully at the moment a window leaves,
// because a daemon that is still alive might have other clients or might simply
// not have finished shutting down; telling those apart means waiting, and
// waiting long enough to be safe made every quit slow.
//
// Counting bridges instead answers it outright. There is one bridge process
// inside the container per window, each carrying its own token, so a window can
// exclude itself and see immediately whether anyone else is there. No polling,
// no grace period, and no guessing.
func OtherWindowsAttached(ctx context.Context, rt Runtime, id, workspaceDir, myToken string) (int, error) {
	if myToken == "" {
		return 0, errors.New("no client token: cannot tell this window's bridge from another's")
	}
	// Bridges carry the workspace path; the daemon carries it too, hence the
	// exclusion. Counting by token would be neater but a process list is all
	// there is to go on.
	n, err := rt.ProcessCount(ctx, id, "--client "+myToken, "")
	if err != nil {
		return 0, err
	}
	total, err := rt.ProcessCount(ctx, id, bridgeMarker(workspaceDir), "--daemon")
	if err != nil {
		return 0, err
	}
	return total - n, nil
}

// bridgeMarker is what a bridge process's command line contains and a daemon's
// does not — the workspace path immediately followed by the client flag.
func bridgeMarker(workspaceDir string) string { return workspaceDir + " --client " }
