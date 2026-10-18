package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/indiejames/indigo/internal/config"
	"github.com/indiejames/indigo/internal/container"
)

// sessionProbeTimeout bounds looking for a running dev container session. It
// runs before every window opens in a workspace that has a devcontainer.json,
// so it has to be short: a wedged docker daemon must cost a pause, not a hang.
const sessionProbeTimeout = 3 * time.Second

// joinRunningDevcontainer handles `indigo <path>` without --devcontainer in a
// workspace whose dev container already has an indigo session running, and
// returns the workspace the window should use.
//
// Left alone, this window would start a second server on the host. The two
// servers hold separate copies of the same files: edits in one never reach the
// other, the last save wins, and nothing says so. So this either joins the
// session (devcontainer_auto_join, or the user says yes) by setting
// useDevcontainer, exactly as if --devcontainer had been passed, or warns.
//
// hostWorkDir may be a subdirectory of the dev container's folder, and the
// session one for the folder above it — `indigo --devcontainer .` at the
// repository root, then `indigo .` in a package. Joining then returns the
// session's workspace, not hostWorkDir: a server of the window's own for the
// subdirectory, even inside the container, would be a second copy of files
// the session already holds.
//
// Joining goes through the full --devcontainer path rather than attaching to
// the found container directly, because the bridge has to run as the same
// user as the server already inside: the server's socket is per-uid, so
// attaching as anyone else would start a second server in the container and
// recreate the problem one level down. devcontainer.json's remoteUser is what
// says who that is, and only the devcontainer CLI resolves it.
func joinRunningDevcontainer(cfg *config.Config, hostWorkDir string) string {
	if containerName != "" || useDevcontainer {
		return hostWorkDir
	}
	root, ok := container.FindDevcontainerRoot(hostWorkDir)
	if !ok {
		return hostWorkDir
	}
	if _, _, err := container.RuntimePath(); err != nil {
		return hostWorkDir
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionProbeTimeout)
	defer cancel()
	session, found, err := container.FindDevcontainerSession(ctx, container.Docker{}, root, hostWorkDir)
	if err != nil || !found {
		// An error here is docker being down or slow, which also means there
		// is no session to warn about that this window could reach.
		return hostWorkDir
	}
	probedState[session.HostWorkspaceDir] = devcontainerProbe{devcontainerSession, session}

	canJoin := container.CLI{}.Available()
	interactive := term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
	if decideJoin(cfg.DevcontainerAutoJoin, canJoin, interactive, os.Stdin, os.Stderr, session.HostWorkspaceDir, session) {
		useDevcontainer = true
		return session.HostWorkspaceDir
	}
	return hostWorkDir
}

// decideJoin reports whether to join, writing whatever the user needs to see
// to out. Split from joinRunningDevcontainer so the decision can be tested
// without docker or a terminal.
func decideJoin(autoJoin, canJoin, interactive bool, in io.Reader, out io.Writer,
	hostWorkDir string, s container.Session) bool {
	if !canJoin {
		// Somebody attached this container another way (--container, or the
		// CLI has since been uninstalled). Joining needs the CLI, so the most
		// this window can do is say what it is about to cause.
		_, _ = fmt.Fprintf(out, "indigo: warning: an indigo session is running in dev container %s for %s.\n"+
			"  This window will use a separate server on this machine, so edits made here and\n"+
			"  there will not see each other. Install the devcontainer CLI to join it instead.\n",
			shortID(s.ContainerID), hostWorkDir)
		return false
	}
	if autoJoin {
		// Silent here: bringUpDevcontainer says "joining" next.
		return true
	}
	_, _ = fmt.Fprintf(out, "indigo: warning: an indigo session is running in dev container %s for %s.\n"+
		"  Opening this window outside it starts a separate server on this machine, so edits\n"+
		"  made here and in the container will not see each other and the last save wins.\n"+
		"  Set devcontainer_auto_join = true in config.toml to always join.\n",
		shortID(s.ContainerID), hostWorkDir)
	if !interactive {
		// No one to ask, and nobody watching the terminal either once the
		// editor starts — but a script or a pipe keeps stderr, so the
		// warning above is still worth having written.
		return false
	}
	_, _ = fmt.Fprint(out, "Join the dev container session? [Y/n] ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		// End of input (Ctrl+D) is not a yes: Enter is how to accept.
		_, _ = fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

// devcontainerState is what is already running for a workspace's dev
// container, as far as a quick probe can tell.
type devcontainerState int

const (
	// devcontainerUnknown covers both "nothing running" and "could not
	// tell": either way `devcontainer up` may have to create or start it.
	devcontainerUnknown devcontainerState = iota
	// devcontainerRunning is a running container with no indigo session for
	// this workspace inside it.
	devcontainerRunning
	// devcontainerSession is a running container with an indigo server for
	// this workspace, which this window will join.
	devcontainerSession
)

// devcontainerProbe is what probeDevcontainer found. session is set only with
// devcontainerSession.
type devcontainerProbe struct {
	state   devcontainerState
	session container.Session
}

// probedState caches probeDevcontainer's answer by folder, because a window
// that joinRunningDevcontainer decided to join asks again in
// bringUpDevcontainer a moment later, and a docker round trip is not free.
var probedState = map[string]devcontainerProbe{}

// probeDevcontainer reports what is already running for hostWorkDir's dev
// container, whose configuration is in configRoot. A session counts when its
// workspace is hostWorkDir or a folder above it; see
// container.FindDevcontainerSession. Bounded by sessionProbeTimeout; any
// failure reads as unknown.
func probeDevcontainer(configRoot, hostWorkDir string) devcontainerProbe {
	if p, ok := probedState[hostWorkDir]; ok {
		return p
	}
	var p devcontainerProbe
	if _, _, err := container.RuntimePath(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), sessionProbeTimeout)
		defer cancel()
		d := container.Docker{}
		if s, found, err := container.FindDevcontainerSession(ctx, d, configRoot, hostWorkDir); err == nil && found {
			p = devcontainerProbe{devcontainerSession, s}
		} else if id, err := d.RunningDevcontainer(ctx, configRoot); err == nil && id != "" {
			p.state = devcontainerRunning
		}
	}
	probedState[hostWorkDir] = p
	return p
}

// devcontainerStartMessage is the line printed before `devcontainer up`. It
// says what is about to happen, because "starting" a container that is
// already running — with "this can take a while" — reads as though the
// existing session is about to be replaced.
func devcontainerStartMessage(hostWorkDir string, state devcontainerState) string {
	switch state {
	case devcontainerSession:
		return fmt.Sprintf("joining the indigo session in the running dev container for %s", hostWorkDir)
	case devcontainerRunning:
		return fmt.Sprintf("attaching to the running dev container for %s", hostWorkDir)
	default:
		return fmt.Sprintf("starting dev container for %s (this can take a while the first time)", hostWorkDir)
	}
}

// shortID trims a container id to the 12 characters docker shows.
func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
