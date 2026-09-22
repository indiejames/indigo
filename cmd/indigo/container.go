package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/indiejames/indigo/internal/app"
	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/container"
	"github.com/indiejames/indigo/internal/server"
)

// Container attachment, set by --container / --container-dir.
//
// Package-level rather than threaded through, because every startup path needs
// the same answer to one question — where is the server — and the alternative
// is the same two parameters on four functions that otherwise have nothing to
// do with containers.
var (
	containerName string
	containerDir  string
	// useDevcontainer is set by --devcontainer. Opt-in rather than automatic on
	// finding a .devcontainer/devcontainer.json: building and starting a
	// container is slow and has side effects, and a repository that happens to
	// ship one is not consent to do that every time someone opens a file in it.
	// VS Code asks; a terminal editor's equivalent of asking is a flag.
	useDevcontainer bool
	// remoteUser is devcontainer.json's remoteUser, so the server runs as the
	// image's dev user rather than root.
	remoteUser string
	// projectServerPath comes from customizations.indigo.serverPath, for an
	// image that already ships a server.
	projectServerPath string
	// stopOnExit is devcontainer.json's shutdownAction, resolved. See
	// container.Configuration.ShouldStopOnExit.
	stopOnExit bool
	// composeProject is set when the dev container is compose-based, where
	// stopping is a project-wide operation indigo does not attempt.
	composeProject bool
	// clientToken identifies this window's bridge process inside the container.
	clientToken string
)

// pathMap translates between the workspace as this machine names it and as the
// container does. Zero value when not attached, and inert in that state.
var pathMap container.PathMap

// resolveWorkspace settles where the workspace is, from both sides, and returns
// the paths the rest of startup should use.
//
// Attached to a container, the client works in *container* paths from here on.
// That is the whole of the translation, and it is small for a reason worth
// stating: the client no longer resolves a workspace path against its own
// filesystem, so paths are only sent, shown, or made relative. The two genuine
// edges are the path the host's shell completed on the command line, and the
// recent-files list, which is host-side state keyed by where the workspace
// lives here. See container.PathMap.
func resolveWorkspace(hostWorkDir, hostTarget string) (workDir, target string) {
	if containerName == "" && !useDevcontainer {
		return hostWorkDir, hostTarget
	}
	if useDevcontainer {
		bringUpDevcontainer(hostWorkDir)
	}
	inContainer := containerDir
	if inContainer == "" {
		// Right for the common `-v $PWD:$PWD` mount and wrong for anything
		// else, which is what --container-dir is for.
		inContainer = hostWorkDir
	}
	pathMap = container.NewPathMap(hostWorkDir, inContainer)

	// The recent-files list stays keyed by the host path, so that two projects
	// which both mount at /workspaces/api do not share one list.
	app.SetRecentRoot(pathMap.HostRoot)

	workDir = pathMap.ContainerRoot
	target = hostTarget
	if hostTarget != "" {
		translated, ok := pathMap.ToContainer(hostTarget)
		if !ok {
			fatalf("%s is outside the workspace %s, so the container cannot see it"+
				" (use --container-dir if the workspace is mounted elsewhere)",
				hostTarget, pathMap.HostRoot)
		}
		target = translated
	}
	return workDir, target
}

// attachTimeout bounds getting a server running inside a container: an
// architecture probe, possibly a 5 MB copy, and a process start. Generous,
// because the copy is the slow part and a cold container image can make even
// the probe wait.
const attachTimeout = 120 * time.Second

// devcontainerUpTimeout bounds `devcontainer up`. Long, because a cold image
// means a pull, a build and every lifecycle hook — this is the slowest thing
// indigo ever waits for, and cutting it short would abandon a build that was
// about to succeed.
const devcontainerUpTimeout = 30 * time.Minute

// parseContainerFlags strips the container flags out of args and returns the
// rest, so the existing positional parsing below is untouched.
//
// Hand-rolled to match the rest of this command, which reads os.Args directly
// rather than using the flag package — swapping one for the other is a bigger
// change than this feature warrants, and a half-converted command would be
// worse than either.
func parseContainerFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--container":
			if i+1 < len(args) {
				containerName = args[i+1]
				i++
			} else {
				fatalf("--container needs a container name or id")
			}
		case "--devcontainer":
			useDevcontainer = true
		case "--container-dir":
			if i+1 < len(args) {
				containerDir = args[i+1]
				i++
			} else {
				fatalf("--container-dir needs a path inside the container")
			}
		default:
			out = append(out, args[i])
		}
	}
	if containerDir != "" && containerName == "" && !useDevcontainer {
		fatalf("--container-dir only means something with --container or --devcontainer")
	}
	if containerName != "" && useDevcontainer {
		fatalf("--container attaches to a container you already have; --devcontainer starts one from devcontainer.json — pick one")
	}
	return out
}

// bringUpDevcontainer builds and starts the container described by
// devcontainer.json, and records what it reports.
//
// The CLI owns lifecycle and configuration; the four facts it returns are
// everything needed to connect, and read-configuration resolves the variable
// substitution and feature merging that are the hard parts of the spec. What
// the CLI does *not* get is the data channel — see container.CLI.
func bringUpDevcontainer(hostWorkDir string) {
	cli := container.CLI{}
	if !cli.Available() {
		fatalf("%v", container.ErrCLIMissing)
	}
	fmt.Fprintf(os.Stderr, "indigo: starting dev container for %s (this can take a while the first time)\n", hostWorkDir)

	ctx, cancel := context.WithTimeout(context.Background(), devcontainerUpTimeout)
	defer cancel()
	res, err := cli.Up(ctx, hostWorkDir)
	if err != nil {
		fatalf("%v", err)
	}
	containerName = res.ContainerID
	remoteUser = res.RemoteUser
	if containerDir == "" {
		// The CLI knows where it mounted the workspace, so there is nothing to
		// guess and --container-dir is only an override.
		containerDir = res.RemoteWorkspaceFolder
	}

	// Optional, and deliberately not fatal: no customizations block is the
	// normal case, and failing to start an editor over an absent settings
	// section would be the wrong trade.
	cfgCtx, cfgCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cfgCancel()
	// stopOnExit defaults to true when the configuration cannot be read at all:
	// the specification's default is to stop, and a container left running
	// because an *optional* settings read failed is the surprising direction.
	stopOnExit = true
	if cfg, err := cli.ReadConfiguration(cfgCtx, hostWorkDir); err == nil {
		projectServerPath = cfg.Customizations.Indigo.ServerPath
		stopOnExit = cfg.ShouldStopOnExit()
		composeProject = cfg.IsCompose()
	}
}

// connect returns an RPC to the server that owns workDir, starting or
// attaching to one as needed.
//
// Two shapes, one caller. Without --container the server is a local process
// behind a unix socket, started on demand. With it, the server runs inside the
// container and the connection is its stdio — see internal/container.
func connect(workDir string) (*client.RPC, error) {
	if containerName == "" {
		sockPath := server.SocketPath(workDir)
		if !server.IsRunning(sockPath) {
			startServer(workDir)
		}
		if err := waitForServer(sockPath, 3*time.Second); err != nil {
			return nil, fmt.Errorf("server did not start: %w", err)
		}
		rpc, err := client.Dial(sockPath)
		if err != nil {
			return nil, fmt.Errorf("connect to server: %w", err)
		}
		return rpc, nil
	}

	// workDir is already the container's name for the workspace — see
	// resolveWorkspace, which is where the translation happens.
	// Checked here as well as in CLI.Up, because --container never goes near
	// the devcontainer CLI. Without it the failure surfaces as a raw
	// "exec: docker: executable file not found in $PATH" wrapped in two layers
	// of context about architecture probing, which says nothing about what to
	// do — seen on a real run before this check existed.
	if _, _, rtErr := container.RuntimePath(); rtErr != nil {
		return nil, rtErr
	}

	ctx, cancel := context.WithTimeout(context.Background(), attachTimeout)
	defer cancel()
	clientToken = newClientToken()
	opts := container.AttachOptions{
		ServerPath:  projectServerPath,
		ClientToken: clientToken,
		Locate:      container.LocateServerBinary,
		Warn:        func(msg string) { fmt.Fprintf(os.Stderr, "indigo: %s\n", msg) },
	}

	// Plugins run wherever the server runs, so the user's have to be carried
	// in. Failing to stage them is not worth refusing to open an editor over:
	// a session without plugins still edits files.
	if staged, err := stagePluginsForContainer(); err != nil {
		fmt.Fprintf(os.Stderr, "indigo: could not prepare plugins for the container: %v\n", err)
	} else if staged != nil {
		defer os.RemoveAll(staged.Dir) //nolint:errcheck
		opts.PluginsDir, opts.PluginsHash = staged.Dir, staged.Hash
		if msg := staged.Describe(); msg != "" {
			fmt.Fprintf(os.Stderr, "indigo: %s\n", msg)
		}
	}

	stream, err := container.Attach(ctx, container.Docker{User: remoteUser}, containerName, workDir, opts)
	if err != nil {
		return nil, fmt.Errorf("attach to container %s: %w", containerName, err)
	}
	rpc, err := client.DialStream(stream)
	if err != nil {
		stream.Close() //nolint:errcheck
		return nil, fmt.Errorf("connect to server in container %s: %w", containerName, err)
	}
	return rpc, nil
}

// shutdownContainer stops a dev container indigo started, once nothing is using
// it. Called after the editor's own program has exited.
//
// Three conditions, each of which matters:
//
//   - **indigo started it.** With --container the user started the container
//     themselves and it is not ours to stop; the specification draws the same
//     line, since shutdownAction describes what the *tool* brought up.
//   - **shutdownAction does not say otherwise.** Absent means stop, which is
//     the specification's default and what VS Code does.
//   - **no other window is still attached.** The server daemon inside the
//     container exits when its last client disconnects, so waiting for it to go
//     is the same question asked in the only place that can answer it
//     truthfully. Stopping while a second window is editing would be a far
//     worse bug than leaving a container running.
func shutdownContainer() {
	if !useDevcontainer || containerName == "" || !stopOnExit {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerStopTimeout)
	defer cancel()

	rt := container.Docker{User: remoteUser}
	others, err := container.OtherWindowsAttached(ctx, rt, containerName, containerDir, clientToken)
	if err != nil {
		// Cannot tell — leave it alone. A container left running is a
		// nuisance; one stopped out from under another window is data loss.
		return
	}
	if others > 0 {
		return
	}

	if composeProject {
		// shutdownAction's default for compose is stopCompose — bringing the
		// whole project down — and stopping only our own service container
		// would look like it worked while leaving the rest running. Saying so
		// is better than doing part of it.
		fmt.Fprintf(os.Stderr, "indigo: dev container is docker-compose based; "+
			"leaving it running (stop it with `docker compose stop`)\n")
		return
	}
	if err := rt.Stop(ctx, containerName); err != nil {
		fmt.Fprintf(os.Stderr, "indigo: could not stop the dev container: %v\n", err)
	}
}

// newClientToken returns a value unique to this window, for marking its bridge
// process. Randomness is only needed to avoid collision between concurrent
// windows, not to be unguessable — it appears in a process list either way.
func newClientToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("pid%d", os.Getpid())
	}
	return hex.EncodeToString(b[:])
}

const (
	// containerStopTimeout bounds the whole shutdown, which is a few execs and
	// a container stop.
	containerStopTimeout = 60 * time.Second
)

// stagePluginsForContainer prepares the user's plugins for the container's
// platform. Returns nil when there is nothing to carry over.
//
// The container's architecture is asked of the container rather than assumed
// from the host's: an arm64 Mac can perfectly well run an amd64 image.
func stagePluginsForContainer() (*container.StagedPlugins, error) {
	hostDir, err := pluginsHostDir()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	goarch, err := (container.Docker{User: remoteUser}).Arch(ctx, containerName)
	if err != nil {
		return nil, err
	}
	staged, err := container.StagePlugins(hostDir, "linux", goarch)
	if err != nil {
		return nil, err
	}
	if staged.Dir == "" {
		return nil, nil
	}
	return staged, nil
}

// pluginsHostDir is where this machine keeps installed plugins, matching the
// server's own resolution so the two cannot drift.
func pluginsHostDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "indigo", "plugins"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "indigo", "plugins"), nil
}
