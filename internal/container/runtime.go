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
	// Exec starts argv inside the container with no TTY, and returns its
	// stdin/stdout as one stream. Closing the stream ends the process.
	Exec(ctx context.Context, id string, argv []string) (io.ReadWriteCloser, error)
}

// ServerPath is where the server binary is placed inside the container.
//
// /tmp rather than the user's home: a dev container's remoteUser may have no
// home directory, and /tmp is writable in every image worth attaching to. The
// name carries the architecture so a container that outlives an indigo upgrade
// on a different host does not run a stale binary from a different one.
func ServerPath(goarch string) string {
	return "/tmp/.indigo-server-" + goarch
}

// AttachOptions carries the parts of an attach that vary.
type AttachOptions struct {
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
// onto a container pays nothing. That check is a stat and not a checksum:
// re-copying on every attach would be a 5 MB write each time, and a stale
// binary is addressed by the architecture-stamped name plus the fact that
// containers rarely outlive an upgrade. A --force style refresh belongs with
// whatever eventually manages versions.
func Attach(ctx context.Context, rt Runtime, id, workspaceDir string, opts AttachOptions) (io.ReadWriteCloser, error) {
	goarch, err := rt.Arch(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("determine container architecture: %w", err)
	}
	remote := opts.ServerPath
	if remote == "" {
		remote = ServerPath(goarch)
	}

	present, err := rt.FileExists(ctx, id, remote)
	if err != nil {
		return nil, fmt.Errorf("check for %s in container: %w", remote, err)
	}
	if !present {
		if opts.Locate == nil {
			return nil, fmt.Errorf("no server at %s in the container and no way to supply one", remote)
		}
		local, err := opts.Locate(goarch)
		if err != nil {
			return nil, err
		}
		if err := rt.CopyIn(ctx, id, local, remote); err != nil {
			return nil, fmt.Errorf("copy server into container: %w", err)
		}
	}

	stream, err := rt.Exec(ctx, id, []string{remote, workspaceDir})
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
