package container

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Plugins run where the server runs, which in a dev container is inside it —
// and the user's plugins are on the host. So they have to be carried across.
//
// The manifest format already anticipates this: `[binaries]` is a map keyed by
// "goos/goarch" and every bundled plugin declares linux entries alongside the
// darwin ones. What was missing was anyone producing the Linux builds
// (`make build-plugins-linux`) and anyone carrying them over.
//
// Only the binary matching the container's platform is staged, not all four —
// a plugin is about 5 MB per platform, and copying every one would be most of
// the transfer for none of the benefit.

// pluginManifest is the part of plugin.toml this needs: which file to use on
// which platform. Everything else is the server's business.
type pluginManifest struct {
	Name     string            `toml:"name"`
	Binaries map[string]string `toml:"binaries"`
}

// StagedPlugins is a directory holding the plugins to copy into a container,
// plus a hash of its contents.
type StagedPlugins struct {
	// Dir is a temporary directory the caller should remove.
	Dir string
	// Hash identifies this exact set of plugins and binaries, so the
	// destination inside the container is content-addressed and an upgraded
	// plugin cannot leave the old one running — the same lesson the server
	// binary taught.
	Hash string
	// Staged and Skipped name the plugins that will and will not be carried
	// over. Skipped is not an error: a plugin with no build for the container's
	// platform is a normal thing to have installed, and the user is told rather
	// than left wondering why it is missing.
	Staged  []string
	Skipped []string
}

// StagePlugins copies the plugins in hostDir that have a binary for goos/goarch
// into a temporary directory, ready to be copied into a container.
//
// A plugin with no matching binary is skipped rather than failing the attach:
// the alternative is refusing to open an editor because one optional plugin was
// built for the wrong platform.
func StagePlugins(hostDir, goos, goarch string) (*StagedPlugins, error) {
	entries, err := os.ReadDir(hostDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &StagedPlugins{}, nil // no plugins installed at all
		}
		return nil, err
	}

	stageDir, err := os.MkdirTemp("", "indigo-plugins-")
	if err != nil {
		return nil, err
	}
	// MkdirTemp creates 0700, and `docker cp` preserves the source mode — so
	// without this the directory arrives unreadable by any container user
	// other than root, and a server running as the image's remoteUser cannot
	// even list it. That is not hypothetical: it is how plugins silently
	// failed to load in a devcontainer whose remoteUser is `vscode`.
	//
	// These are plugin binaries staged for copying, not secrets.
	if err := os.Chmod(stageDir, 0o755); err != nil {
		os.RemoveAll(stageDir) //nolint:errcheck
		return nil, err
	}
	out := &StagedPlugins{Dir: stageDir}
	key := goos + "/" + goarch
	hasher := sha256.New()

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Sorted so the hash depends on the plugins, not on directory order.
	sort.Strings(names)

	for _, name := range names {
		src := filepath.Join(hostDir, name)
		var manifest pluginManifest
		manifestPath := filepath.Join(src, "plugin.toml")
		if _, err := toml.DecodeFile(manifestPath, &manifest); err != nil {
			out.Skipped = append(out.Skipped, name)
			continue
		}
		rel, ok := manifest.Binaries[key]
		if !ok || rel == "" {
			out.Skipped = append(out.Skipped, name)
			continue
		}
		binary := filepath.Join(src, rel)
		if info, err := os.Stat(binary); err != nil || info.IsDir() {
			out.Skipped = append(out.Skipped, name)
			continue
		}

		dst := filepath.Join(stageDir, name)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			os.RemoveAll(stageDir) //nolint:errcheck
			return nil, err
		}
		if err := copyFile(manifestPath, filepath.Join(dst, "plugin.toml"), 0o644); err != nil {
			os.RemoveAll(stageDir) //nolint:errcheck
			return nil, err
		}
		if err := copyFile(binary, filepath.Join(dst, rel), 0o755); err != nil {
			os.RemoveAll(stageDir) //nolint:errcheck
			return nil, err
		}

		binHash, err := hashFile(binary)
		if err != nil {
			os.RemoveAll(stageDir) //nolint:errcheck
			return nil, err
		}
		// Writing to a hash cannot fail; the name and hash go in together so a
		// plugin being renamed changes the set as much as its contents do.
		hasher.Write([]byte(name + ":" + binHash + "\n")) //nolint:errcheck
		out.Staged = append(out.Staged, name)
	}

	if len(out.Staged) == 0 {
		os.RemoveAll(stageDir) //nolint:errcheck
		out.Dir = ""
		return out, nil
	}
	out.Hash = hex.EncodeToString(hasher.Sum(nil)[:8])
	return out, nil
}

// PluginsPath is where a staged plugin set is placed inside the container.
// Content-addressed for the same reason the server binary is.
func PluginsPath(hash string) string { return "/tmp/.indigo-plugins-" + hash }

// Describe summarises what will and will not be carried over, for telling the
// user once at startup. Empty when there is nothing worth saying.
func (s *StagedPlugins) Describe() string {
	if len(s.Skipped) == 0 {
		return ""
	}
	return fmt.Sprintf("plugins not available in the container (no build for its platform): %s",
		strings.Join(s.Skipped, ", "))
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close() //nolint:errcheck
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close() //nolint:errcheck
		return err
	}
	return out.Close()
}
