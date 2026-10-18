package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// Finding a dev container session that is already running, so that a window
// started without --devcontainer can join it instead of starting a second
// server on the host.
//
// The failure this exists for is quiet. A window started with --devcontainer
// talks to a server inside the container; `indigo .` in the same directory
// looks for a server by a socket on the host, finds none, and starts its own.
// Two servers then hold independent copies of the same files, edits made in
// one never reach the other, and the last save wins.

// devcontainerLabel is the label the devcontainer CLI puts on every container
// it creates, naming the host folder it was created for. VS Code's dev
// container support uses the same CLI and therefore the same label.
const devcontainerLabel = "devcontainer.local_folder"

// HasDevcontainerConfig reports whether dir has a devcontainer.json in any of
// the places the specification looks: .devcontainer/devcontainer.json,
// .devcontainer.json, or one level down in .devcontainer/<name>/.
//
// This is the cheap gate in front of everything else here. Most workspaces have
// none, and those must not pay for a docker call on every start.
func HasDevcontainerConfig(dir string) bool {
	for _, p := range []string{
		filepath.Join(dir, ".devcontainer", "devcontainer.json"),
		filepath.Join(dir, ".devcontainer.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".devcontainer", "*", "devcontainer.json"))
	return len(matches) > 0
}

// FindDevcontainerRoot returns the nearest directory at or above dir that has
// a devcontainer.json, which is the folder the devcontainer CLI has to be
// given: it looks for the configuration only in the folder it is handed, so
// `devcontainer up` on a subdirectory fails, and a container is labelled with
// the folder it was created for. ok is false when no directory up to the
// filesystem root has one.
func FindDevcontainerRoot(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	for {
		if HasDevcontainerConfig(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// RunningDevcontainer returns the id of a running container the devcontainer
// CLI created for hostFolder, or "" when there is none. Stopped containers are
// not reported: there is nothing to join in one.
func (d Docker) RunningDevcontainer(ctx context.Context, hostFolder string) (string, error) {
	out, err := d.output(ctx, "ps", "-q", "--filter", "label="+devcontainerLabel+"="+hostFolder)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], nil
}

// MountedAt returns where hostPath appears inside container id, read from the
// container's mounts. ok is false when no mount covers it.
func (d Docker) MountedAt(ctx context.Context, id, hostPath string) (string, bool, error) {
	out, err := d.output(ctx, "inspect", "-f",
		`{{range .Mounts}}{{.Source}}{{"\t"}}{{.Destination}}{{"\n"}}{{end}}`, id)
	if err != nil {
		return "", false, err
	}
	dest, ok := mountedAt(out, hostPath)
	return dest, ok, nil
}

// mountedAt resolves hostPath against `docker inspect` mount lines of the form
// "source\tdestination". The longest covering source wins, so a mount of the
// workspace itself beats a mount of one of its parents.
func mountedAt(mounts, hostPath string) (string, bool) {
	hostPath = filepath.Clean(hostPath)
	best, bestLen := "", -1
	for _, line := range strings.Split(mounts, "\n") {
		src, dst, found := strings.Cut(line, "\t")
		if !found || src == "" || dst == "" {
			continue
		}
		src = filepath.Clean(src)
		var rel string
		switch {
		case hostPath == src:
			rel = ""
		case strings.HasPrefix(hostPath, src+string(filepath.Separator)) || src == "/":
			rel = strings.TrimPrefix(hostPath, src)
		default:
			continue
		}
		if len(src) > bestLen {
			best = filepath.Join(filepath.Clean(dst), rel)
			bestLen = len(src)
		}
	}
	return best, bestLen >= 0
}

// sessionProbe is the part of a container runtime FindDevcontainerSession
// needs, so it can be tested without docker.
type sessionProbe interface {
	RunningDevcontainer(ctx context.Context, hostFolder string) (string, error)
	MountedAt(ctx context.Context, id, hostPath string) (string, bool, error)
	ServerWorkspaces(ctx context.Context, id string) ([]string, error)
}

// Session is an indigo server already running inside a dev container for a
// host workspace.
type Session struct {
	ContainerID string
	// WorkspaceDir is the workspace as the container names it — the path the
	// server inside was started on.
	WorkspaceDir string
	// HostWorkspaceDir is the same workspace as this machine names it. It can
	// be a parent of the folder that was asked about: a window opened on a
	// subdirectory joins the server already running for the repository.
	HostWorkspaceDir string
}

// FindDevcontainerSession reports whether the dev container created for
// configRoot (the folder holding devcontainer.json; see FindDevcontainerRoot)
// is running with an indigo server whose workspace is hostDir or a folder
// above it.
//
// A parent counts because that server already holds hostDir's files. A window
// that started its own server for the subdirectory — inside the container or
// on this machine — would keep a second copy of them, and the two would
// overwrite each other's saves. When servers run for several folders above
// hostDir, the nearest one wins, as it would for a window opened there.
//
// A running container alone is not enough: someone using VS Code, or a
// container whose indigo windows have all closed, has no server to join, and
// suggesting one would send them into a container for nothing. A server for a
// folder beside hostDir, or below it, does not count either: it does not hold
// the files a window here would open, or not all of them.
func FindDevcontainerSession(ctx context.Context, p sessionProbe, configRoot, hostDir string) (Session, bool, error) {
	configRoot, hostDir = filepath.Clean(configRoot), filepath.Clean(hostDir)
	id, err := p.RunningDevcontainer(ctx, configRoot)
	if err != nil || id == "" {
		return Session{}, false, err
	}
	dir, ok, err := p.MountedAt(ctx, id, hostDir)
	if err != nil || !ok {
		return Session{}, false, err
	}
	servers, err := p.ServerWorkspaces(ctx, id)
	if err != nil {
		return Session{}, false, err
	}
	var best Session
	for _, ws := range servers {
		ws = filepath.Clean(ws)
		if len(ws) <= len(best.WorkspaceDir) {
			continue
		}
		host, ok := hostFolderFor(hostDir, dir, ws)
		if !ok || !within(host, configRoot) {
			// Outside the dev container's folder: a server for some other
			// mount, which this window has no business joining.
			continue
		}
		best = Session{ContainerID: id, WorkspaceDir: ws, HostWorkspaceDir: host}
	}
	return best, best.WorkspaceDir != "", nil
}

// hostFolderFor maps ws, a container folder at or above dir, back to this
// machine, given that hostDir appears in the container as dir. ok is false
// when ws is not at or above dir, or the mapping cannot be made by walking up
// the same number of folders on both sides.
func hostFolderFor(hostDir, dir, ws string) (string, bool) {
	if !within(dir, ws) {
		return "", false
	}
	rel, err := filepath.Rel(ws, dir)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return hostDir, true
	}
	suffix := string(filepath.Separator) + rel
	if !strings.HasSuffix(hostDir, suffix) {
		return "", false
	}
	return strings.TrimSuffix(hostDir, suffix), true
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	return path == dir || dir == "/" || strings.HasPrefix(path, dir+string(filepath.Separator))
}
