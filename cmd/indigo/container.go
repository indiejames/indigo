package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/indiejames/indigo/internal/app"
	"github.com/indiejames/indigo/internal/client"
	"github.com/indiejames/indigo/internal/container"
	"github.com/indiejames/indigo/internal/debuglog"
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
	// remoteEnv is devcontainer.json's remoteEnv, unresolved. Resolved against
	// the container at attach time — see container.ResolveRemoteEnv.
	remoteEnv map[string]*string
	// devcontainerRoot is the folder `devcontainer up` was run on, so a
	// reconnect can bring a stopped container back up the same way.
	devcontainerRoot string
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
		hostWorkDir = bringUpDevcontainer(hostWorkDir)
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
// devcontainer.json, records what it reports, and returns the workspace the
// window should use.
//
// The CLI owns lifecycle and configuration; the four facts it returns are
// everything needed to connect, and read-configuration resolves the variable
// substitution and feature merging that are the hard parts of the spec. What
// the CLI does *not* get is the data channel — see container.CLI.
//
// hostWorkDir can be a subdirectory of the folder holding devcontainer.json.
// The CLI is given that folder, since it looks for the configuration nowhere
// else and labels the container with it. When a session is already running
// for a folder above hostWorkDir, the window joins it and that folder becomes
// its workspace, for the reason joinRunningDevcontainer gives.
func bringUpDevcontainer(hostWorkDir string) string {
	cli := container.CLI{}
	if !cli.Available() {
		fatalf("%v", container.ErrCLIMissing)
	}
	root, ok := container.FindDevcontainerRoot(hostWorkDir)
	if !ok {
		// The CLI says what is missing better than a second message would.
		root = hostWorkDir
	}
	devcontainerRoot = root
	probe := probeDevcontainer(root, hostWorkDir)
	sessionDir := ""
	if probe.state == devcontainerSession && containerDir == "" {
		// Not with --container-dir: that names where hostWorkDir itself is
		// mounted, and moving the workspace would make it wrong.
		hostWorkDir, sessionDir = probe.session.HostWorkspaceDir, probe.session.WorkspaceDir
	}
	fmt.Fprintf(os.Stderr, "indigo: %s\n", devcontainerStartMessage(hostWorkDir, probe.state))

	ctx, cancel := context.WithTimeout(context.Background(), devcontainerUpTimeout)
	defer cancel()
	res, err := cli.Up(ctx, root)
	if err != nil {
		fatalf("%v", err)
	}
	containerName = res.ContainerID
	remoteUser = res.RemoteUser
	if containerDir == "" {
		// The CLI knows where it mounted the workspace, so there is nothing to
		// guess and --container-dir is only an override.
		containerDir = workspaceInContainer(res.RemoteWorkspaceFolder, root, hostWorkDir, sessionDir)
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
	if cfg, err := cli.ReadConfiguration(cfgCtx, root); err == nil {
		projectServerPath = cfg.Customizations.Indigo.ServerPath
		stopOnExit = cfg.ShouldStopOnExit()
		composeProject = cfg.IsCompose()
		remoteEnv = cfg.RemoteEnv
	}
	return hostWorkDir
}

// workspaceInContainer is where hostWorkDir is inside the container, given
// that root — the folder `devcontainer up` was run on — is at remoteFolder.
// A running session's own workspace wins when there is one, because that is
// the exact path its server was started on and the bridge must name it.
func workspaceInContainer(remoteFolder, root, hostWorkDir, sessionDir string) string {
	if sessionDir != "" {
		return sessionDir
	}
	rel, err := filepath.Rel(root, hostWorkDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return remoteFolder
	}
	return path.Join(remoteFolder, filepath.ToSlash(rel))
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
	stream, serverBuild, err := attachContainer(ctx, workDir, startupNotice)
	if err != nil {
		return nil, err
	}
	// The container keeps one server per workspace, and a window joins one
	// already running rather than starting its own. Naming the build this
	// attach started lets that server say when it is an older one.
	rpc, err := client.DialStreamExpecting(stream, serverBuild)
	if err != nil {
		stream.Close() //nolint:errcheck
		return nil, fmt.Errorf("connect to server in container %s: %w", containerName, err)
	}
	return rpc, nil
}

// startupNotice prints a message about attaching to the terminal, which is
// where it belongs before the editor has taken over the screen.
func startupNotice(msg string) { fmt.Fprintf(os.Stderr, "indigo: %s\n", msg) }

// attachContainer gets this window's server running in the container and
// returns the stream to it, with the server build it started ("" for a
// project's own server). notice receives what the attach has to say: the
// terminal at startup, the log on a reconnect, when the editor owns the screen.
func attachContainer(ctx context.Context, workDir string, notice func(string)) (io.ReadWriteCloser, string, error) {
	var serverBuild string
	opts := container.AttachOptions{
		ServerPath:  projectServerPath,
		ClientToken: clientToken,
		Locate:      container.LocateServerBinary,
		Warn:        notice,
		ServerBuild: func(build string) { serverBuild = build },
	}

	if len(remoteEnv) > 0 {
		opts.Env = serverRemoteEnv(ctx, notice)
	}

	// Plugins run wherever the server runs, so the user's have to be carried
	// in. Failing to stage them is not worth refusing to open an editor over:
	// a session without plugins still edits files.
	if staged, err := stagePluginsForContainer(); err != nil {
		notice(fmt.Sprintf("could not prepare plugins for the container: %v", err))
	} else if staged != nil {
		defer os.RemoveAll(staged.Dir) //nolint:errcheck
		opts.PluginsDir, opts.PluginsHash = staged.Dir, staged.Hash
		if msg := staged.Describe(); msg != "" {
			notice(msg)
		}
	}

	id := currentContainer()
	stream, err := container.Attach(ctx, container.Docker{User: remoteUser}, id, workDir, opts)
	if err != nil {
		return nil, "", fmt.Errorf("attach to container %s: %w", id, err)
	}
	return stream, serverBuild, nil
}

// armReconnect lets a container window survive its connection dropping, by
// attaching again. A no-op for a server on this machine, where a dropped
// connection means the server itself has gone. Called once the editor is
// running; see RPC.SetRedial.
func armReconnect(rpc *client.RPC, workDir string) {
	if containerName == "" {
		return
	}
	notice := func(msg string) { debuglog.Write("container", "%s", msg) }
	rpc.SetRedial(func(ctx context.Context) (io.ReadWriteCloser, string, error) {
		if err := ensureContainerRunning(ctx); err != nil {
			return nil, "", err
		}
		return attachContainer(ctx, workDir, notice)
	})
}

// ensureContainerRunning brings back a dev container that stopped while the
// window was disconnected, such as when Docker Desktop restarted its VM.
//
// Only one indigo started: a container named with --container is the user's,
// and starting it is theirs to decide, so a stopped one fails the attempt.
func ensureContainerRunning(ctx context.Context) error {
	id := currentContainer()
	running, err := (container.Docker{User: remoteUser}).IsRunning(ctx, id)
	if err == nil && running {
		return nil
	}
	if !useDevcontainer {
		if err != nil {
			return err
		}
		return fmt.Errorf("container %s is not running", id)
	}
	debuglog.Write("container", "dev container %s is not running; bringing it up", id)
	res, err := (container.CLI{}).Up(ctx, devcontainerRoot)
	if err != nil {
		return err
	}
	setContainer(res.ContainerID)
	return nil
}

// containerMu guards containerName once the editor is running, when a
// reconnect (ensureContainerRunning) may replace it on one goroutine while the
// window quits (shutdownContainer) on another. Startup reads it directly,
// before either exists.
var containerMu sync.Mutex

func currentContainer() string {
	containerMu.Lock()
	defer containerMu.Unlock()
	return containerName
}

func setContainer(id string) {
	containerMu.Lock()
	containerName = id
	containerMu.Unlock()
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
	containerName := currentContainer()
	if !useDevcontainer || containerName == "" || !stopOnExit {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), containerStopTimeout)
	defer cancel()

	rt := container.Docker{User: remoteUser}
	others, err := container.OtherWindowsAttached(ctx, rt, containerName, clientToken)
	if err != nil {
		// Cannot tell — leave it alone. A container left running is a
		// nuisance; one stopped out from under another window is data loss.
		return
	}
	// Anything but a clean zero is a reason not to act. A negative count means
	// this window's own bridges outnumbered the total, which cannot happen if
	// the two probes agreed — so the picture is inconsistent and the same
	// asymmetry applies: leave it running.
	if others != 0 {
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

// serverRemoteEnv resolves remoteEnv against the container's environment.
//
// Not fatal when the container's environment cannot be read: the references
// then resolve to their defaults (usually empty), which for the common
// "${containerEnv:PATH}:/extra" would *replace* PATH with just "/extra" and
// break every tool on it. So the entries that need the container are dropped
// instead, and the ones that do not are still applied.
func serverRemoteEnv(ctx context.Context, notice func(string)) []string {
	cenv, err := (container.Docker{User: remoteUser}).InspectEnv(ctx, currentContainer())
	if err != nil {
		notice(fmt.Sprintf("could not read the container's environment, "+
			"so remoteEnv entries that refer to it are not applied: %v", err))
		return container.ResolveRemoteEnv(container.WithoutContainerEnvRefs(remoteEnv), nil, os.LookupEnv)
	}
	return container.ResolveRemoteEnv(remoteEnv, cenv, os.LookupEnv)
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

// pluginsHostDir is where this machine keeps installed plugins, matching
// plugin.pluginsConfigDir's resolution so the two cannot drift.
func pluginsHostDir() (string, error) {
	// Same first check the server makes. Without it, someone who points
	// INDIGO_PLUGINS_DIR at their plugins gets them for a local session and
	// silently none in a container, because staging looked somewhere else.
	if d := os.Getenv("INDIGO_PLUGINS_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "indigo", "plugins"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "indigo", "plugins"), nil
}
