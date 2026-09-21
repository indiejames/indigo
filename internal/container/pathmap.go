package container

import (
	"path/filepath"
	"strings"
)

// PathMap translates between the workspace as the host names it and as the
// container names it.
//
// # Why there are only a handful of call sites
//
// The obvious implementation of container support translates paths at every
// RPC — there are more than seventy, many carry paths, and several carry them
// *back* (diagnostics, definitions, references, workspace edits). That is a lot
// of places to be wrong in, and being wrong in one of them is a path that
// silently fails to open.
//
// It is also unnecessary, because of where bite 2 left the client: it no longer
// resolves a workspace path against its own filesystem at all. Paths are only
// sent to the server, shown to the user, or made relative to the workspace
// root. So the client can simply work in *container* paths throughout, and the
// translation shrinks to the two genuine edges — the path named on the command
// line, which the host's shell completed, and the recent-files list, which is
// host-side state.
//
// This is also what the user sees in VS Code: attached to a container, the
// paths it shows are the container's.
//
// # Semantics
//
// Both ends are POSIX here — the host is macOS or Linux and the container is
// Linux — so slash-separated cleaning is correct for both. A path outside the
// workspace root is returned unchanged and reported as untranslated, because
// the honest answer for a file the container cannot see is not a made-up path.
type PathMap struct {
	// HostRoot and ContainerRoot are the same directory, named from each side.
	HostRoot      string
	ContainerRoot string
}

// NewPathMap cleans both roots and returns the map between them.
func NewPathMap(hostRoot, containerRoot string) PathMap {
	return PathMap{
		HostRoot:      cleanRoot(hostRoot),
		ContainerRoot: cleanRoot(containerRoot),
	}
}

// Identity reports whether the two sides name the workspace the same way, which
// is the `-v $PWD:$PWD` case and needs no translation at all.
func (m PathMap) Identity() bool { return m.HostRoot == m.ContainerRoot }

// ToContainer maps a host path into the container. ok is false when the path
// lies outside the workspace, in which case p is returned unchanged — there is
// no meaningful container path for a file that is not mounted, and inventing
// one would turn "this file is not in the container" into a confusing failure
// further in.
func (m PathMap) ToContainer(p string) (string, bool) {
	return rebase(p, m.HostRoot, m.ContainerRoot)
}

// ToHost maps a container path back out to the host, with the same contract.
func (m PathMap) ToHost(p string) (string, bool) {
	return rebase(p, m.ContainerRoot, m.HostRoot)
}

func rebase(p, from, to string) (string, bool) {
	if p == "" || from == "" || to == "" {
		return p, false
	}
	clean := filepath.Clean(p)
	switch {
	case clean == from:
		return to, true
	// The separator matters: without it "/home/me/proj" would also swallow
	// "/home/me/project", which is a neighbouring directory and not inside the
	// workspace at all.
	case strings.HasPrefix(clean, from+string(filepath.Separator)):
		rel := clean[len(from)+1:]
		return filepath.Join(to, rel), true
	default:
		return p, false
	}
}

func cleanRoot(p string) string {
	if p == "" {
		return ""
	}
	c := filepath.Clean(p)
	// Clean leaves "/" as "/", where the separator logic above would produce a
	// doubled slash; treating the filesystem root as a workspace root is
	// nonsense anyway, so it is normalised to empty and the map becomes inert.
	if c == string(filepath.Separator) {
		return ""
	}
	return c
}
