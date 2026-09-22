package rpcclient

import (
	"context"
	"errors"

	proto "github.com/indiejames/indigo/internal/proto"
	"github.com/indiejames/indigo/internal/workspacefs"
)

// The workspace-filesystem calls. See internal/server/server_workspacefs.go for
// why the server owns the workspace and the client asks it.

// DirEntry is one item in a directory listing.
type DirEntry struct {
	Name  string
	IsDir bool
}

// ListDir returns one directory's entries from the server. An empty path means
// the workspace root, which spares a caller from having to know what that is on
// the server's side of a container boundary.
func (r *RPC) ListDir(ctx context.Context, path string) ([]DirEntry, error) {
	fut, rel := r.svc.ListDir(ctx, func(p proto.EditorService_listDir_Params) error {
		return p.SetPath(path)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Entries()
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, list.Len())
	for i := range out {
		item := list.At(i)
		name, err := item.Name()
		if err != nil {
			return nil, err
		}
		out[i] = DirEntry{Name: name, IsDir: item.IsDir()}
	}
	return out, nil
}

// ListWorkspaceFiles returns every file under the workspace root, as
// workspace-relative paths.
func (r *RPC) ListWorkspaceFiles(ctx context.Context) ([]string, error) {
	fut, rel := r.svc.ListWorkspaceFiles(ctx, func(_ proto.EditorService_listWorkspaceFiles_Params) error {
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Paths()
	if err != nil {
		return nil, err
	}
	out := make([]string, list.Len())
	for i := range out {
		p, err := list.At(i)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// GrepWorkspace searches the workspace on the server.
//
// explicit selects the caller-supplied caseSensitive/isRegex over the
// smart-case and backslash-prefix conventions the pattern itself carries —
// the same two entry points the engine has always had.
//
// A pattern error (a malformed regex — a user typo) comes back as the second
// return value, a non-empty search-error string from a successful call, while
// the error return is reserved for RPC failures — so a typo cannot be mistaken
// for the connection being in trouble.
func (r *RPC) GrepWorkspace(ctx context.Context, pattern, include, exclude string, caseSensitive, isRegex, explicit bool) ([]workspacefs.Result, string, error) {
	fut, rel := r.svc.GrepWorkspace(ctx, func(p proto.EditorService_grepWorkspace_Params) error {
		if err := p.SetPattern(pattern); err != nil {
			return err
		}
		if err := p.SetInclude(include); err != nil {
			return err
		}
		if err := p.SetExclude(exclude); err != nil {
			return err
		}
		p.SetCaseSensitive(caseSensitive)
		p.SetIsRegex(isRegex)
		p.SetExplicit(explicit)
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, "", err
	}
	if msg, err := res.Error(); err != nil {
		return nil, "", err
	} else if msg != "" {
		return nil, msg, nil
	}
	list, err := res.Results()
	if err != nil {
		return nil, "", err
	}
	out := make([]workspacefs.Result, list.Len())
	for i := range out {
		m := list.At(i)
		relPath, err := m.RelPath()
		if err != nil {
			return nil, "", err
		}
		text, err := m.LineText()
		if err != nil {
			return nil, "", err
		}
		out[i] = workspacefs.Result{
			RelPath:  relPath,
			Line:     int(m.Line()),
			Col:      int(m.Col()),
			MatchLen: int(m.MatchLen()),
			LineText: text,
		}
	}
	return out, "", nil
}

// FilterWorkspaceFiles keeps the workspace-relative paths that still exist, are
// not under an ignored directory, and are not gitignored.
func (r *RPC) FilterWorkspaceFiles(ctx context.Context, rels []string) ([]string, error) {
	fut, rel := r.svc.FilterWorkspaceFiles(ctx, func(p proto.EditorService_filterWorkspaceFiles_Params) error {
		list, err := p.NewPaths(int32(len(rels)))
		if err != nil {
			return err
		}
		for i, v := range rels {
			if err := list.Set(i, v); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return nil, err
	}
	list, err := res.Kept()
	if err != nil {
		return nil, err
	}
	out := make([]string, list.Len())
	for i := range out {
		p, err := list.At(i)
		if err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// StatPath reports whether path exists on the server's filesystem and whether
// it is a directory.
//
// A missing path is (false, false, nil): absent is an answer, not a failure.
// The returned error covers a permission denial or an I/O fault, which callers
// treat differently — offering to create something that is merely missing is
// reasonable, guessing past a permission error is not.
func (r *RPC) StatPath(ctx context.Context, path string) (exists, isDir bool, err error) {
	fut, rel := r.svc.StatPath(ctx, func(p proto.EditorService_statPath_Params) error {
		return p.SetPath(path)
	})
	defer rel()
	res, ferr := fut.Struct()
	if ferr != nil {
		return false, false, ferr
	}
	if msg, merr := res.Error(); merr != nil {
		return false, false, merr
	} else if msg != "" {
		return false, false, errors.New(msg)
	}
	return res.Exists(), res.IsDir(), nil
}

// CreateDir creates dir and any missing parents on the server's filesystem.
func (r *RPC) CreateDir(ctx context.Context, dir string) error {
	fut, rel := r.svc.CreateDir(ctx, func(p proto.EditorService_createDir_Params) error {
		return p.SetPath(dir)
	})
	defer rel()
	res, err := fut.Struct()
	if err != nil {
		return err
	}
	msg, err := res.Error()
	if err != nil {
		return err
	}
	if msg != "" {
		return errors.New(msg)
	}
	return nil
}

// SetIgnoredDirs tells the server which extra directory names to hide from the
// picker, recent files and workspace grep. See the schema for why the client
// owns this rather than the server reading it from its own config.
func (r *RPC) SetIgnoredDirs(ctx context.Context, dirs []string) error {
	fut, rel := r.svc.SetIgnoredDirs(ctx, func(p proto.EditorService_setIgnoredDirs_Params) error {
		list, err := p.NewDirs(int32(len(dirs)))
		if err != nil {
			return err
		}
		for i, d := range dirs {
			if err := list.Set(i, d); err != nil {
				return err
			}
		}
		return nil
	})
	defer rel()
	_, err := fut.Struct()
	return err
}
