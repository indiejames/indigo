package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	proto "github.com/indiejames/indigo/internal/proto"
	"github.com/indiejames/indigo/internal/workspacefs"
)

// The workspace-filesystem RPCs. These exist so the editor's view of the
// workspace comes from the process that can actually see it.
//
// The client used to do all of this itself — walk the tree for the picker,
// spawn ripgrep for workspace search — which is correct exactly as long as the
// client and the files are on the same machine. Once the server runs inside a
// dev container that stops being true: the paths differ, and with a
// named-volume workspace or a remote Docker host the files are not on the
// client at all. Both VS Code and Neovim put file listing and search on the
// remote end for the same reason.
//
// Every handler here calls call.Go() before doing any work. A tree walk or a
// ripgrep run takes seconds on a large workspace, and capnp serialises a
// connection's calls until a handler releases the queue — so without it a
// single grep would stall every edit, poll and save on that connection. This is
// the head-of-line blocking the hang detector was built to report, and it would
// be entirely self-inflicted here.

// ListDir returns one directory's entries, for the picker's browse mode.
//
// An empty path means the workspace root, so a client that has not yet resolved
// the container-side root does not have to guess it.
//
// Ignored directories are filtered here rather than by the caller, because the
// ignore set is config-driven and the config that describes this filesystem is
// this process's. Browse mode has always hidden them; doing it client-side
// would mean the client deciding what to hide in a tree it cannot see.
func (s *editorService) ListDir(_ context.Context, call proto.EditorService_listDir) error {
	rel, err := call.Args().Path()
	if err != nil {
		return err
	}
	path, err := s.resolveWorkspaceRel(rel)
	if err != nil {
		return err
	}
	call.Go()

	entries, err := workspacefs.ListDir(path)
	if err != nil {
		return err
	}
	ignored := workspacefs.IgnoredDirs()
	kept := entries[:0]
	for _, e := range entries {
		if e.IsDir && ignored[e.Name] {
			continue
		}
		kept = append(kept, e)
	}
	entries = kept
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewEntries(int32(len(entries)))
	if err != nil {
		return err
	}
	for i, e := range entries {
		item := list.At(i)
		if err := item.SetName(e.Name); err != nil {
			return err
		}
		item.SetIsDir(e.IsDir)
	}
	return nil
}

// resolveWorkspaceRel turns a workspace-relative path from a client into an
// absolute one inside the workspace.
//
// The client sends paths relative to the workspace (the picker's currentDir),
// and an earlier version passed them to the filesystem unchanged — which
// resolves them against *this process's* working directory. That happened to
// work only because the server is started with the workspace as its cwd, which
// is a coincidence of how it is launched and not a property anything guarantees.
//
// It also refuses to leave the workspace. A client is not an attacker here, but
// the server is reachable over a socket by anything running as this user, and
// answering "list /etc" because someone sent "../../etc" is not a thing a
// workspace server should do.
func (s *editorService) resolveWorkspaceRel(rel string) (string, error) {
	if rel == "" {
		return s.workspaceDir, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the workspace", rel)
	}
	abs := filepath.Clean(filepath.Join(s.workspaceDir, rel))
	root := filepath.Clean(s.workspaceDir)
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", rel)
	}
	return abs, nil
}

// ListWorkspaceFiles enumerates every file under the workspace root as
// workspace-relative paths, for the picker's fuzzy search.
func (s *editorService) ListWorkspaceFiles(_ context.Context, call proto.EditorService_listWorkspaceFiles) error {
	call.Go()

	paths := workspacefs.CollectFiles(s.workspaceDir, workspacefs.IgnoredDirs())
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewPaths(int32(len(paths)))
	if err != nil {
		return err
	}
	for i, p := range paths {
		if err := list.Set(i, p); err != nil {
			return err
		}
	}
	return nil
}

// GrepWorkspace searches the workspace.
//
// A pattern error comes back in the result's error field rather than as an RPC
// failure. Search runs on every keystroke while someone types a regex, so a
// half-typed one is an ordinary occurrence and not a fault of the call — and an
// RPC error would reach the client through the failure path, which is where
// genuine connection trouble lives.
func (s *editorService) GrepWorkspace(_ context.Context, call proto.EditorService_grepWorkspace) error {
	args := call.Args()
	pattern, err := args.Pattern()
	if err != nil {
		return err
	}
	include, err := args.Include()
	if err != nil {
		return err
	}
	exclude, err := args.Exclude()
	if err != nil {
		return err
	}
	explicit, caseSensitive, isRegex := args.Explicit(), args.CaseSensitive(), args.IsRegex()
	call.Go()

	var results []workspacefs.Result
	var searchErr error
	if explicit {
		results, searchErr = workspacefs.SearchExplicit(s.workspaceDir, pattern, include, exclude, caseSensitive, isRegex)
	} else {
		results, searchErr = workspacefs.Search(s.workspaceDir, pattern, include, exclude)
	}

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if searchErr != nil {
		return res.SetError(searchErr.Error())
	}
	list, err := res.NewResults(int32(len(results)))
	if err != nil {
		return err
	}
	for i, r := range results {
		m := list.At(i)
		if err := m.SetRelPath(r.RelPath); err != nil {
			return err
		}
		m.SetLine(uint32(r.Line))
		m.SetCol(uint32(r.Col))
		m.SetMatchLen(uint32(r.MatchLen))
		if err := m.SetLineText(r.LineText); err != nil {
			return err
		}
	}
	return nil
}

// FilterWorkspaceFiles keeps the paths that still exist, are not under an
// ignored directory, and are not gitignored — the recent-files filter.
func (s *editorService) FilterWorkspaceFiles(_ context.Context, call proto.EditorService_filterWorkspaceFiles) error {
	in, err := call.Args().Paths()
	if err != nil {
		return err
	}
	rels := make([]string, in.Len())
	for i := range rels {
		p, err := in.At(i)
		if err != nil {
			return err
		}
		rels[i] = p
	}
	call.Go()

	kept := workspacefs.FilterExisting(s.workspaceDir, rels)
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	list, err := res.NewKept(int32(len(kept)))
	if err != nil {
		return err
	}
	for i, p := range kept {
		if err := list.Set(i, p); err != nil {
			return err
		}
	}
	return nil
}

// StatPath reports whether a path exists and whether it is a directory.
func (s *editorService) StatPath(_ context.Context, call proto.EditorService_statPath) error {
	path, err := call.Args().Path()
	if err != nil {
		return err
	}
	call.Go()

	exists, isDir, statErr := workspacefs.Stat(path)
	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if statErr != nil {
		return res.SetError(statErr.Error())
	}
	res.SetExists(exists)
	res.SetIsDir(isDir)
	return nil
}

// CreateDir creates a directory and any missing parents.
func (s *editorService) CreateDir(_ context.Context, call proto.EditorService_createDir) error {
	path, err := call.Args().Path()
	if err != nil {
		return err
	}
	call.Go()

	res, err := call.AllocResults()
	if err != nil {
		return err
	}
	if mkErr := workspacefs.CreateDir(path); mkErr != nil {
		return res.SetError(mkErr.Error())
	}
	return nil
}

// SetIgnoredDirs replaces the configured extra ignore names.
//
// The client owns this setting: it is a preference about what the user wants to
// see rather than a property of the filesystem, and in a container the server's
// own config belongs to the image and not to them. Pushing it also restores the
// hot-reload that moving the file listing to the server had quietly taken away
// — the client watches config.toml already.
func (s *editorService) SetIgnoredDirs(_ context.Context, call proto.EditorService_setIgnoredDirs) error {
	list, err := call.Args().Dirs()
	if err != nil {
		return err
	}
	dirs := make([]string, list.Len())
	for i := range dirs {
		d, err := list.At(i)
		if err != nil {
			return err
		}
		dirs[i] = d
	}
	workspacefs.SetIgnoredDirs(dirs)
	return nil
}
